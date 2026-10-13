package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

type Launch struct {
	Command string
	Args    []string
	Dir     string
	Env     []string
}
type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("Agent RPC error (%d): %s", e.Code, e.Message) }

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      uint64          `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *RPCError       `json:"error"`
	Ready   bool            `json:"ready"`
}
type reply struct {
	result json.RawMessage
	err    error
}
type Handshake struct {
	ProtocolVersion      int      `json:"protocolVersion"`
	AgentProtocolVersion int      `json:"agentProtocolVersion"`
	Capabilities         []string `json:"capabilities"`
}

func (h Handshake) Supports(capability string) bool {
	for _, c := range h.Capabilities {
		if c == capability {
			return true
		}
	}
	return false
}

// Runtime correlates concurrent responses and fails every in-flight request on
// EOF. Each connection owns a process: reconnect never replays user operations.
type Runtime struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	mu        sync.Mutex
	writeMu   sync.Mutex
	pending   map[uint64]chan reply
	failed    error
	sequence  atomic.Uint64
	done      chan struct{}
	stopOnce  sync.Once
	Handshake Handshake
}

func Start(ctx context.Context, launch Launch) (*Runtime, error) {
	cmd := exec.Command(launch.Command, launch.Args...)
	cmd.Dir = launch.Dir
	if launch.Env != nil {
		cmd.Env = launch.Env
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	// Driver diagnostics can include credentials; never return raw stderr.
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return nil, fmt.Errorf("start agent: %w", err)
	}
	r := &Runtime{cmd: cmd, stdin: stdin, pending: map[uint64]chan reply{}, done: make(chan struct{})}
	go r.read(stdout)
	go func() {
		err := cmd.Wait()
		if err == nil {
			err = errors.New("agent process exited")
		}
		r.fail(err)
		close(r.done)
	}()
	var h Handshake
	err = r.Call(ctx, "handshake", map[string]any{"protocolVersion": 2, "agentProtocolVersion": 2, "appVersion": "go-wails"}, &h)
	if err != nil {
		var rpc *RPCError
		if !errors.As(err, &rpc) || rpc.Code != -32601 {
			r.Close()
			return nil, fmt.Errorf("agent handshake: %w", err)
		}
		h.ProtocolVersion = 1 // Legacy agents without handshake use connect.
	}
	if h.ProtocolVersion < 1 || h.ProtocolVersion > 2 {
		r.Close()
		return nil, errors.New("unsupported agent protocol version")
	}
	r.Handshake = h
	return r, nil
}
func (r *Runtime) read(stdout io.Reader) {
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), 32<<20)
	for scanner.Scan() {
		var response response
		if err := json.Unmarshal(scanner.Bytes(), &response); err != nil {
			r.fail(errors.New("invalid agent JSON response"))
			r.kill()
			return
		}
		if response.Ready && response.ID == 0 {
			continue
		}
		if response.ID == 0 || response.JSONRPC != "" && response.JSONRPC != "2.0" {
			r.fail(errors.New("invalid agent protocol response"))
			r.kill()
			return
		}
		r.mu.Lock()
		ch := r.pending[response.ID]
		delete(r.pending, response.ID)
		r.mu.Unlock()
		if ch != nil {
			var err error
			if response.Error != nil {
				err = response.Error
			} else if response.Result == nil {
				err = errors.New("agent response has no result")
			}
			ch <- reply{result: response.Result, err: err}
		}
	}
	err := scanner.Err()
	if err == nil {
		err = errors.New("agent protocol stream closed")
	}
	r.fail(err)
	r.kill()
}
func (r *Runtime) fail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failed != nil {
		return
	}
	r.failed = err
	for id, ch := range r.pending {
		ch <- reply{err: err}
		delete(r.pending, id)
	}
}
func (r *Runtime) Call(ctx context.Context, method string, params any, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	id := r.sequence.Add(1)
	data, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return err
	}
	data = append(data, '\n')
	ch := make(chan reply, 1)
	r.mu.Lock()
	if r.failed != nil {
		err := r.failed
		r.mu.Unlock()
		return err
	}
	r.pending[id] = ch
	r.mu.Unlock()
	defer func() { r.mu.Lock(); delete(r.pending, id); r.mu.Unlock() }()
	written := make(chan error, 1)
	go func() { r.writeMu.Lock(); _, err := r.stdin.Write(data); r.writeMu.Unlock(); written <- err }()
	select {
	case err := <-written:
		if err != nil {
			r.fail(err)
			r.kill()
			return err
		}
	case <-ctx.Done():
		r.fail(ctx.Err())
		r.kill()
		return ctx.Err()
	}
	select {
	case response := <-ch:
		if response.err != nil {
			return response.err
		}
		if out == nil {
			return nil
		}
		if err := json.Unmarshal(response.result, out); err != nil {
			r.fail(errors.New("invalid agent result"))
			r.kill()
			return fmt.Errorf("decode agent result: %w", err)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (r *Runtime) Failed() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.failed != nil }
func (r *Runtime) kill() {
	r.stopOnce.Do(func() {
		r.stdin.Close()
		if r.cmd.Process != nil {
			_ = r.cmd.Process.Kill()
		}
	})
}
func (r *Runtime) Close() error {
	r.fail(errors.New("agent runtime closed"))
	r.kill()
	select {
	case <-r.done:
		return nil
	case <-time.After(3 * time.Second):
		return errors.New("agent did not exit after termination")
	}
}

type Session struct {
	Runtime *Runtime
	ID      string
	gate    chan struct{}
	closed  atomic.Bool
}

func OpenSession(ctx context.Context, launch Launch, params map[string]any) (*Session, error) {
	r, err := Start(ctx, launch)
	if err != nil {
		return nil, err
	}
	s := &Session{Runtime: r, ID: fmt.Sprintf("go-%d-%d", time.Now().UnixNano(), r.cmd.Process.Pid), gate: make(chan struct{}, 1)}
	method := "connect"
	if r.Handshake.ProtocolVersion >= 2 && r.Handshake.Supports("multi_session") {
		method = "open_session"
		params = cloneParams(params)
		params["agentSessionId"] = s.ID
	}
	if err = r.Call(ctx, method, params, nil); err != nil {
		r.Close()
		return nil, err
	}
	return s, nil
}
func cloneParams(params map[string]any) map[string]any {
	copy := make(map[string]any, len(params)+1)
	for k, v := range params {
		copy[k] = v
	}
	return copy
}
func (s *Session) Call(ctx context.Context, method string, params map[string]any, out any) error {
	select {
	case s.gate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-s.gate }()
	if s.closed.Load() {
		return errors.New("agent connection is closed; reconnect required")
	}
	params = cloneParams(params)
	if s.Runtime.Handshake.ProtocolVersion >= 2 && s.Runtime.Handshake.Supports("multi_session") {
		params["agentSessionId"] = s.ID
	}
	err := s.Runtime.Call(ctx, method, params, out)
	if ctx.Err() != nil {
		// The operation may have reached the database. Cancel within a separate
		// boundary, then terminate this connection's process and require reconnect.
		cancelCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		cancelMethod := "cancel_session"
		if params["agentSessionId"] == nil {
			cancelMethod = "cancel"
		}
		_ = s.Runtime.Call(cancelCtx, cancelMethod, map[string]any{"agentSessionId": s.ID}, nil)
		cancel()
		s.closed.Store(true)
		_ = s.Runtime.Close()
		return ctx.Err()
	}
	var rpc *RPCError
	if errors.As(err, &rpc) {
		if s.Runtime.Handshake.Supports("structured_error_v1") {
			expectedSession := ""
			if params["agentSessionId"] != nil {
				expectedSession = s.ID
			}
			if contractErr := validateRecovery(rpc.Data, expectedSession); contractErr != nil {
				s.closed.Store(true)
				_ = s.Runtime.Close()
				return contractErr
			}
		}
		var recovery struct {
			Disposition string `json:"sessionDisposition"`
		}
		if json.Unmarshal(rpc.Data, &recovery) == nil && (recovery.Disposition == "quarantine" || recovery.Disposition == "replace_runtime") {
			s.closed.Store(true)
			_ = s.Runtime.Close()
		}
	}
	return err
}
func (s *Session) Close() error {
	if !s.closed.Swap(true) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		method := "disconnect"
		if s.Runtime.Handshake.Supports("multi_session") {
			method = "close_session"
		}
		_ = s.Runtime.Call(ctx, method, map[string]any{"agentSessionId": s.ID}, nil)
		cancel()
	}
	return s.Runtime.Close()
}
