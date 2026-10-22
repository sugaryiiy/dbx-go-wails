package database

import (
	"context"
	"crypto/ed25519"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestSSHSingleHopOwnsConnection(t *testing.T) {
	target := sshTestEchoServer(t)
	server := newSSHTestServer(t, func(s *sshTestServer) { s.allow = map[string]bool{target: true} })
	dial, err := networkDialer(Config{Type: "postgres", Transports: []map[string]any{server.layer()}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn, err := dial(ctx, "tcp", target)
	if err != nil {
		t.Fatal(err)
	}
	cancel() // Dial contexts do not own an already established connection.
	sshAssertEcho(t, conn)
	conn.Close()
	conn.Close()
	server.waitIdle(t)
}
func TestSSHMultiHop(t *testing.T) {
	target := sshTestEchoServer(t)
	second := newSSHTestServer(t, func(s *sshTestServer) { s.allow = map[string]bool{target: true} })
	first := newSSHTestServer(t, func(s *sshTestServer) { s.allow = map[string]bool{second.listener.Addr().String(): true} })
	config := Config{Type: "mysql", Transports: []map[string]any{first.layer(), {"type": "proxy", "enabled": false}, second.layer()}}
	dial, err := networkDialer(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := dial(ctx, "tcp", target)
	if err != nil {
		t.Fatal(err)
	}
	sshAssertEcho(t, conn)
	conn.Close()
	first.waitIdle(t)
	second.waitIdle(t)
	message, err := TestSSHTransports(ctx, config)
	if err != nil || !strings.Contains(message, "2 hop") {
		t.Fatalf("test = %q, %v", message, err)
	}
	first.waitIdle(t)
	second.waitIdle(t)
}
func TestSSHHostVerification(t *testing.T) {
	server := newSSHTestServer(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	other, _ := sshTestSigner(t)
	layer := server.layer()
	layer["host_key_fingerprint"] = ssh.FingerprintSHA256(other.PublicKey())
	if _, err := TestSSHTunnel(ctx, layer); err == nil || !strings.Contains(err.Error(), ssh.FingerprintSHA256(server.signer.PublicKey())) {
		t.Fatalf("mismatch error = %v", err)
	}
	if got := server.passwordAttempts.Load(); got != 0 {
		t.Fatalf("credentials sent before host verification: %d", got)
	}
	delete(layer, "host_key_fingerprint")
	layer["known_hosts_path"] = filepath.Join(t.TempDir(), "known_hosts")
	if _, err := TestSSHTunnel(ctx, layer); err == nil || !strings.Contains(err.Error(), "unknown, changed, or revoked") {
		t.Fatalf("unknown error = %v", err)
	}
	if _, err := os.Stat(layer["known_hosts_path"].(string)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unknown host must never be persisted: %v", err)
	}
	content := knownhosts.Line([]string{server.listener.Addr().String()}, server.signer.PublicKey()) + "\n"
	if err := os.WriteFile(layer["known_hosts_path"].(string), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := TestSSHTunnel(ctx, layer); err != nil {
		t.Fatal(err)
	}
	content = knownhosts.Line([]string{server.listener.Addr().String()}, other.PublicKey()) + "\n"
	if err := os.WriteFile(layer["known_hosts_path"].(string), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := TestSSHTunnel(ctx, layer); err == nil {
		t.Fatal("changed known_hosts key accepted")
	}
	server.waitIdle(t)
}
func TestSSHAuthentication(t *testing.T) {
	signer, key := sshTestSigner(t)
	keyDir := t.TempDir()
	keyPath := sshWritePrivateKey(t, keyDir, "key", key, "")
	encryptedPath := sshWritePrivateKey(t, keyDir, "encrypted", key, "passphrase")
	server := newSSHTestServer(t, func(s *sshTestServer) {
		s.config.PublicKeyCallback = func(meta ssh.ConnMetadata, pub ssh.PublicKey) (*ssh.Permissions, error) {
			if meta.User() == "tester" && string(pub.Marshal()) == string(signer.PublicKey().Marshal()) {
				return nil, nil
			}
			return nil, io.EOF
		}
	})
	for _, test := range []struct {
		name, method, path, password, passphrase string
		fail                                     bool
	}{
		{"key", "key", keyPath, "wrong", "", false},
		{"encrypted-key", "key", encryptedPath, "", "passphrase", false},
		{"bad-passphrase", "key", encryptedPath, "", "wrong", true},
		{"password-ignores-key", "password", "/does/not/exist", "test-secret", "", false},
		{"wrong-password-does-not-use-key", "password", keyPath, "wrong", "", true},
		{"key-and-password", "key+password", keyPath, "test-secret", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			layer := server.layer()
			layer["auth_method"] = test.method
			layer["key_path"] = test.path
			layer["password"] = test.password
			layer["key_passphrase"] = test.passphrase
			// The encrypted-key subtests spend most of their time in the
			// key KDF (~2.8s locally under -race), so a 3s budget flakes
			// on loaded CI runners; match the production default (15s).
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_, err := TestSSHTunnel(ctx, layer)
			if (err != nil) != test.fail {
				t.Fatalf("authentication err=%v, want failure=%v", err, test.fail)
			}
			server.waitIdle(t)
		})
	}
	t.Run("key-rejected-password-fallback", func(t *testing.T) {
		passwordOnly := newSSHTestServer(t, func(s *sshTestServer) {
			s.config.PublicKeyCallback = func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) { return nil, io.EOF }
		})
		layer := passwordOnly.layer()
		layer["auth_method"] = "key+password"
		layer["key_path"] = keyPath
		if _, err := TestSSHTunnel(context.Background(), layer); err != nil {
			t.Fatal(err)
		}
		if passwordOnly.passwordAttempts.Load() != 1 {
			t.Fatal("password fallback was not used")
		}
		passwordOnly.waitIdle(t)
	})
	t.Run("none", func(t *testing.T) {
		noAuth := newSSHTestServer(t, func(s *sshTestServer) { s.config.NoClientAuth = true })
		layer := noAuth.layer()
		layer["auth_method"] = "none"
		if _, err := TestSSHTunnel(context.Background(), layer); err != nil {
			t.Fatal(err)
		}
		noAuth.waitIdle(t)
	})
	t.Run("agent", func(t *testing.T) {
		sock := sshTestAgent(t, key)
		layer := server.layer()
		layer["auth_method"] = "agent"
		layer["ssh_agent_sock_path"] = sock
		layer["password"] = "wrong"
		layer["key_path"] = "/invalid"
		if _, err := TestSSHTunnel(context.Background(), layer); err != nil {
			t.Fatal(err)
		}
		server.waitIdle(t)
	})
}
func sshWritePrivateKey(t *testing.T, dir, name string, key ed25519.PrivateKey, passphrase string) string {
	t.Helper()
	var block *pem.Block
	var err error
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(key, "fixture")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(key, "fixture", []byte(passphrase))
	}
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err = os.WriteFile(path, pem.EncodeToMemory(block), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func sshTestAgent(t *testing.T, key ed25519.PrivateKey) string {
	t.Helper()
	// A short path also works on macOS's 104-byte Unix socket path limit.
	dir, err := os.MkdirTemp("", "dbx-agent-")
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "agent.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		os.RemoveAll(dir)
		t.Fatal(err)
	}
	keyring := agent.NewKeyring()
	if err = keyring.Add(agent.AddedKey{PrivateKey: key}); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() { defer conn.Close(); agent.ServeAgent(keyring, conn) }()
		}
	}()
	t.Cleanup(func() { listener.Close(); <-done; os.RemoveAll(dir) })
	return socket
}

func TestSSHCancellation(t *testing.T) {
	t.Run("handshake", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		accepted := make(chan net.Conn, 1)
		go func() { conn, _ := listener.Accept(); accepted <- conn }()
		host, port, _ := net.SplitHostPort(listener.Addr().String())
		layer := map[string]any{"host": host, "port": port, "user": "tester", "auth_method": "none", "known_hosts_path": filepath.Join(t.TempDir(), "known_hosts")}
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_, err = TestSSHTunnel(ctx, layer)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("handshake cancellation=%v", err)
		}
		conn := <-accepted
		defer conn.Close()
		conn.SetReadDeadline(time.Now().Add(time.Second))
		_, err = io.Copy(io.Discard, conn)
		if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
			t.Fatal("handshake cancellation leaked connection")
		}
	})
	t.Run("forward", func(t *testing.T) {
		server := newSSHTestServer(t, func(s *sshTestServer) { s.holdForward = true })
		dial, err := networkDialer(Config{Type: "postgres", Transports: []map[string]any{server.layer()}})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		_, err = dial(ctx, "tcp", "127.0.0.1:5432")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("forward cancellation=%v", err)
		}
		server.waitIdle(t)
	})
	t.Run("second-hop-handshake", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		accepted := make(chan net.Conn, 1)
		go func() { conn, _ := listener.Accept(); accepted <- conn }()
		first := newSSHTestServer(t, func(s *sshTestServer) { s.allow = map[string]bool{listener.Addr().String(): true} })
		host, port, _ := net.SplitHostPort(listener.Addr().String())
		second := map[string]any{"type": "ssh", "host": host, "port": port, "user": "tester", "auth_method": "none", "known_hosts_path": filepath.Join(t.TempDir(), "known_hosts")}
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		_, err = TestSSHTransports(ctx, Config{Type: "mysql", Transports: []map[string]any{first.layer(), second}})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("nested handshake cancellation=%v", err)
		}
		conn := <-accepted
		defer conn.Close()
		conn.SetReadDeadline(time.Now().Add(time.Second))
		io.Copy(io.Discard, conn)
		first.waitIdle(t)
	})
}
func TestSSHRejectUnsupportedSettings(t *testing.T) {
	base := map[string]any{"type": "ssh", "host": "localhost", "user": "tester", "auth_method": "none"}
	for _, test := range []struct {
		field string
		value any
	}{
		{"type", "proxy"}, {"type", "http_tunnel"}, {"profile_id", "unresolved"}, {"expose_lan", true}, {"allow_exec_channel_proxy", true}, {"proxy_command", "nc %h %p"}, {"port", -1}, {"connect_timeout_secs", -1}, {"auth_method", "invalid"}, {"host_key_fingerprint", "invalid"}, {"auth_method", "password"}, {"auth_method", "key"}, {"enabled", "false"},
	} {
		t.Run(test.field+"/"+str(map[string]any{"v": test.value}, "v"), func(t *testing.T) {
			p := map[string]any{}
			for k, v := range base {
				p[k] = v
			}
			p[test.field] = test.value
			if err := ValidateNetworkTransports(Config{Type: "postgres", Transports: []map[string]any{p}}); err == nil {
				t.Fatal("unsupported settings accepted")
			}
		})
	}
	if err := ValidateNetworkTransports(Config{Type: "sqlite", Transports: []map[string]any{base}}); err == nil {
		t.Fatal("SSH SQLite accepted")
	}
	if err := ValidateNetworkTransports(Config{Type: "mysql", Transports: []map[string]any{base, base, base, base, base}}); err == nil {
		t.Fatal("too many hops accepted")
	}
	if err := ValidateNetworkTransports(Config{Type: "mysql", Transports: []map[string]any{base, base, base, base}}); err != nil {
		t.Fatal(err)
	}
}

func TestSSHSocketDeadlinesCanBeReset(t *testing.T) {
	target := sshTestEchoServer(t)
	server := newSSHTestServer(t, func(s *sshTestServer) { s.allow = map[string]bool{target: true} })
	dial, err := networkDialer(Config{Type: "postgres", Transports: []map[string]any{server.layer()}})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := dial(context.Background(), "tcp", target)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(30 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	_, err = conn.Read(make([]byte, 1))
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("read deadline = %v", err)
	}
	if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	sshAssertEcho(t, conn)
	conn.Close()
	server.waitIdle(t)
}

func TestSSHClosingOneSocketDoesNotCloseAnother(t *testing.T) {
	target := sshTestEchoServer(t)
	server := newSSHTestServer(t, func(s *sshTestServer) { s.allow = map[string]bool{target: true} })
	dial, err := networkDialer(Config{Type: "postgres", Transports: []map[string]any{server.layer()}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := dial(context.Background(), "tcp", target)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := dial(context.Background(), "tcp", target)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	first.Close()
	if err := second.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	sshAssertEcho(t, second)
	second.Close()
	server.waitIdle(t)
}

func TestSSHRejectsNonRegularAndOversizedFiles(t *testing.T) {
	dir := t.TempDir()
	for _, field := range []string{"key_path", "known_hosts_path"} {
		t.Run(field, func(t *testing.T) {
			layer := map[string]any{"host": "127.0.0.1", "port": 22, "user": "tester", "auth_method": "none", "known_hosts_path": filepath.Join(t.TempDir(), "missing")}
			layer[field] = dir
			if field == "key_path" {
				layer["auth_method"] = "key"
			}
			_, err := TestSSHTunnel(context.Background(), layer)
			if err == nil || !strings.Contains(err.Error(), "regular file") {
				t.Fatalf("non-regular SSH file = %v", err)
			}
			path := filepath.Join(t.TempDir(), "oversized")
			file, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			if err = file.Truncate((4 << 20) + 1); err != nil {
				t.Fatal(err)
			}
			file.Close()
			layer[field] = path
			_, err = TestSSHTunnel(context.Background(), layer)
			if err == nil || !strings.Contains(err.Error(), "exceeds") {
				t.Fatalf("oversized SSH file = %v", err)
			}
		})
	}
}

func TestSSHRejectsPipeWithoutBlocking(t *testing.T) {
	for _, field := range []string{"key_path", "known_hosts_path"} {
		t.Run(field, func(t *testing.T) {
			reader, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			defer writer.Close()
			path := fmt.Sprintf("/dev/fd/%d", reader.Fd())
			if _, err := os.Stat(path); err != nil {
				t.Skip("OS does not expose pipe descriptors under /dev/fd")
			}
			layer := map[string]any{"host": "127.0.0.1", "port": 22, "user": "tester", "auth_method": "none", "known_hosts_path": filepath.Join(t.TempDir(), "missing")}
			layer[field] = path
			if field == "key_path" {
				layer["auth_method"] = "key"
			}
			done := make(chan error, 1)
			go func() { _, err := TestSSHTunnel(context.Background(), layer); done <- err }()
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), "regular file") {
					t.Fatalf("pipe not rejected: %v", err)
				}
			case <-time.After(time.Second):
				writer.Close()
				<-done
				t.Fatal("SSH configuration attempted to read a pipe")
			}
		})
	}
}
