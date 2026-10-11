package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

// A subprocess exercises the real pipes, JSON framing, request correlation and
// process lifecycle. It deliberately returns concurrent responses out of order.
func TestAgentHelperProcess(t *testing.T) {
	if os.Getenv("DBX_AGENT_TEST_HELPER") != "1" {
		return
	}
	var outputMu sync.Mutex
	respond := func(id uint64, result any, rpcErr *RPCError) {
		outputMu.Lock()
		defer outputMu.Unlock()
		envelope := map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
		if rpcErr != nil {
			delete(envelope, "result")
			envelope["error"] = rpcErr
		}
		_ = json.NewEncoder(os.Stdout).Encode(envelope)
	}
	fmt.Println(`{"ready":true}`)
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request struct {
			ID     uint64         `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(2)
		}
		if request.Method == "exit" {
			os.Exit(7)
		}
		go func() {
			switch request.Method {
			case "handshake":
				if os.Getenv("DBX_AGENT_TEST_LEGACY") == "1" {
					respond(request.ID, nil, &RPCError{Code: -32601, Message: "method not found"})
				} else {
					respond(request.ID, map[string]any{"protocolVersion": 2, "agentProtocolVersion": 2, "capabilities": []string{"multi_session", "query", "metadata", "paged_query", "transaction", "kv"}}, nil)
				}
			case "echo":
				delay, _ := request.Params["delay"].(float64)
				time.Sleep(time.Duration(delay) * time.Millisecond)
				respond(request.ID, request.Params, nil)
			case "block":
				select {}
			case "malformed":
				outputMu.Lock()
				fmt.Println("this is not JSON")
				outputMu.Unlock()
			case "quarantine":
				respond(request.ID, nil, &RPCError{Code: -1, Message: "unrecoverable connection", Data: json.RawMessage(`{"sessionDisposition":"quarantine"}`)})
			case "open_session", "connect":
				if os.Getenv("DBX_AGENT_TEST_OPEN_FAIL") == "1" {
					respond(request.ID, nil, &RPCError{Code: -1, Message: "failed open"})
				} else {
					respond(request.ID, map[string]any{"ok": true}, nil)
				}
			default:
				respond(request.ID, map[string]any{"ok": true, "method": request.Method, "params": request.Params}, nil)
			}
		}()
	}
	os.Exit(0)
}
func helperLaunch() Launch {
	return Launch{Command: os.Args[0], Args: []string{"-test.run=^TestAgentHelperProcess$"}, Env: append(os.Environ(), "DBX_AGENT_TEST_HELPER=1")}
}
func TestRuntimeCorrelatesOutOfOrderResponses(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runtime, err := Start(ctx, helperLaunch())
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	var wg sync.WaitGroup
	for index := 0; index < 20; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			var result map[string]any
			err := runtime.Call(ctx, "echo", map[string]any{"value": index, "delay": 20 - index}, &result)
			if err != nil {
				t.Error(err)
				return
			}
			if result["value"] != float64(index) {
				t.Errorf("response routing mismatch: %v", result)
			}
		}(index)
	}
	wg.Wait()
}
func TestRuntimeEOFAndMalformedFailPendingRequests(t *testing.T) {
	for _, method := range []string{"exit", "malformed"} {
		t.Run(method, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			runtime, err := Start(ctx, helperLaunch())
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Close()
			blocked := make(chan error, 1)
			go func() { blocked <- runtime.Call(ctx, "block", nil, nil) }()
			time.Sleep(30 * time.Millisecond)
			if err = runtime.Call(ctx, method, nil, nil); err == nil {
				t.Fatal("protocol failure accepted")
			}
			select {
			case err := <-blocked:
				if err == nil {
					t.Fatal("pending request succeeded")
				}
			case <-time.After(time.Second):
				t.Fatal("pending request leaked after EOF")
			}
		})
	}
}
func TestSessionCancellationQuarantinesAndDoesNotReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	session, err := OpenSession(ctx, helperLaunch(), map[string]any{"host": "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	deadline, stop := context.WithTimeout(ctx, 50*time.Millisecond)
	defer stop()
	err = session.Call(deadline, "block", map[string]any{"sql": "INSERT INTO important VALUES(1)"}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected timeout, got %v", err)
	}
	if !session.Runtime.Failed() {
		t.Fatal("timed-out process retained")
	}
	if err = session.Call(ctx, "echo", nil, nil); err == nil {
		t.Fatal("operation replay/reuse accepted after cancellation")
	}
	select {
	case <-session.Runtime.done:
	case <-time.After(time.Second):
		t.Fatal("process was not reaped")
	}
}
func TestSessionLegacyFallbackAndSessionInjection(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint(legacy), func(t *testing.T) {
			launch := helperLaunch()
			if legacy {
				launch.Env = append(launch.Env, "DBX_AGENT_TEST_LEGACY=1")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			session, err := OpenSession(ctx, launch, map[string]any{"password": "credential"})
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			var result map[string]any
			if err = session.Call(ctx, "echo", map[string]any{"sessionId": "cursor"}, &result); err != nil {
				t.Fatal(err)
			}
			if result["sessionId"] != "cursor" {
				t.Fatal("cursor/session identity conflated")
			}
			if legacy && result["agentSessionId"] != nil {
				t.Fatal("logical session sent to v1")
			}
			if !legacy && result["agentSessionId"] != session.ID {
				t.Fatal("v2 session identity missing")
			}
		})
	}
}
func TestSessionRecoveryDispositionTerminatesProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	session, err := OpenSession(ctx, helperLaunch(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	err = session.Call(ctx, "quarantine", nil, nil)
	if err == nil || !session.Runtime.Failed() {
		t.Fatal("quarantined session was left live")
	}
}
