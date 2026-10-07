package database

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

type sshTestServer struct {
	listener         net.Listener
	signer           ssh.Signer
	config           *ssh.ServerConfig
	allow            map[string]bool
	holdForward      bool
	mu               sync.Mutex
	open             map[net.Conn]struct{}
	wg               sync.WaitGroup
	acceptDone       chan struct{}
	passwordAttempts atomic.Int32
	forwardAttempts  atomic.Int32
}

func sshTestSigner(t *testing.T) (ssh.Signer, ed25519.PrivateKey) {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return signer, key
}

func newSSHTestServer(t *testing.T, configure func(*sshTestServer)) *sshTestServer {
	t.Helper()
	signer, _ := sshTestSigner(t)
	s := &sshTestServer{signer: signer, open: map[net.Conn]struct{}{}, acceptDone: make(chan struct{})}
	s.config = &ssh.ServerConfig{PasswordCallback: func(meta ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
		s.passwordAttempts.Add(1)
		if meta.User() == "tester" && string(password) == "test-secret" {
			return nil, nil
		}
		return nil, io.EOF
	}}
	if configure != nil {
		configure(s)
	}
	s.config.AddHostKey(s.signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.listener = listener
	go func() {
		defer close(s.acceptDone)
		for {
			raw, err := listener.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.open[raw] = struct{}{}
			s.mu.Unlock()
			s.wg.Add(1)
			go s.serve(raw)
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		<-s.acceptDone
		s.mu.Lock()
		for conn := range s.open {
			conn.Close()
		}
		s.mu.Unlock()
		done := make(chan struct{})
		go func() { s.wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("SSH fixture did not shut down")
		}
	})
	return s
}
func (s *sshTestServer) serve(raw net.Conn) {
	defer s.wg.Done()
	defer func() { raw.Close(); s.mu.Lock(); delete(s.open, raw); s.mu.Unlock() }()
	conn, channels, requests, err := ssh.NewServerConn(raw, s.config)
	if err != nil {
		return
	}
	defer conn.Close()
	go ssh.DiscardRequests(requests)
	done := make(chan struct{})
	defer close(done)
	for channel := range channels {
		s.wg.Add(1)
		go func(ch ssh.NewChannel) {
			defer s.wg.Done()
			if ch.ChannelType() != "direct-tcpip" {
				ch.Reject(ssh.UnknownChannelType, "direct-tcpip only")
				return
			}
			s.forwardAttempts.Add(1)
			if s.holdForward {
				<-done
				return
			}
			var req struct {
				Host       string
				Port       uint32
				OriginHost string
				OriginPort uint32
			}
			if err := ssh.Unmarshal(ch.ExtraData(), &req); err != nil {
				ch.Reject(ssh.ConnectionFailed, "invalid request")
				return
			}
			target := net.JoinHostPort(req.Host, strconv.Itoa(int(req.Port)))
			if s.allow != nil && !s.allow[target] {
				ch.Reject(ssh.Prohibited, "target denied")
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			upstream, err := (&net.Dialer{}).DialContext(ctx, "tcp", target)
			cancel()
			if err != nil {
				ch.Reject(ssh.ConnectionFailed, "target unavailable")
				return
			}
			defer upstream.Close()
			downstream, requests, err := ch.Accept()
			if err != nil {
				return
			}
			defer downstream.Close()
			go ssh.DiscardRequests(requests)
			copied := make(chan struct{})
			go func() { io.Copy(upstream, downstream); upstream.Close(); close(copied) }()
			io.Copy(downstream, upstream)
			downstream.Close()
			<-copied
		}(channel)
	}
}
func (s *sshTestServer) layer() map[string]any {
	host, p, _ := net.SplitHostPort(s.listener.Addr().String())
	port, _ := strconv.Atoi(p)
	return map[string]any{"type": "ssh", "host": host, "port": port, "user": "tester", "password": "test-secret", "auth_method": "password", "host_key_fingerprint": ssh.FingerprintSHA256(s.signer.PublicKey())}
}
func (s *sshTestServer) waitIdle(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		count := len(s.open)
		s.mu.Unlock()
		if count == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("SSH connection leaked after closing or cancellation")
}
func sshTestEchoServer(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	open := map[net.Conn]bool{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			open[conn] = true
			mu.Unlock()
			go func() { defer conn.Close(); io.Copy(conn, conn); mu.Lock(); delete(open, conn); mu.Unlock() }()
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		<-done
		mu.Lock()
		for conn := range open {
			conn.Close()
		}
		mu.Unlock()
	})
	return listener.Addr().String()
}
func sshAssertEcho(t *testing.T, conn net.Conn) {
	t.Helper()
	if _, err := conn.Write([]byte("hello SSH")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 9)
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello SSH" {
		t.Fatalf("echo = %q", got)
	}
}
