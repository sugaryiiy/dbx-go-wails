package database

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

const maxSSHTransportHops = 4

type sshLayer struct {
	host, user, password, keyPath, keyPassphrase     string
	authMethod, agentSocket, knownHosts, fingerprint string
	port                                             int
	timeout                                          time.Duration
}

// ValidateNetworkTransports checks configuration without connecting or reading
// private keys. Shared tunnel profiles must be resolved before this boundary.
func ValidateNetworkTransports(c Config) error {
	layers, err := parseSSHLayers(c.Transports)
	if err != nil {
		return err
	}
	if c.Type == "sqlite" && len(layers) != 0 {
		return unsupported("SSH transport for SQLite files")
	}
	return nil
}

func parseSSHLayers(transports []map[string]any) ([]sshLayer, error) {
	var layers []sshLayer
	for i, transport := range transports {
		if enabled, exists := transport["enabled"]; exists && enabled != nil {
			b, ok := enabled.(bool)
			if !ok {
				return nil, fmt.Errorf("transport %d: enabled must be a boolean", i+1)
			}
			if !b {
				continue
			}
		}
		if str(transport, "type") != "ssh" {
			return nil, unsupported("transport type " + str(transport, "type"))
		}
		if len(layers) == maxSSHTransportHops {
			return nil, fmt.Errorf("SSH transport supports at most %d hops", maxSSHTransportHops)
		}
		layer, err := parseSSHLayer(transport)
		if err != nil {
			return nil, fmt.Errorf("SSH hop %d: %w", len(layers)+1, err)
		}
		layers = append(layers, layer)
	}
	return layers, nil
}

func parseSSHLayer(p map[string]any) (sshLayer, error) {
	var l sshLayer
	if kind := str(p, "type"); kind != "" && kind != "ssh" {
		return l, unsupported("transport type " + kind)
	}
	if str(p, "profile_id") != "" {
		return l, errors.New("shared tunnel profile has not been resolved")
	}
	if strings.TrimSpace(str(p, "proxy_command")) != "" {
		return l, unsupported("SSH ProxyCommand")
	}
	for _, field := range []string{"allow_exec_channel_proxy", "expose_lan"} {
		if v, exists := p[field]; exists && v != nil {
			b, ok := v.(bool)
			if !ok {
				return l, fmt.Errorf("%s must be a boolean", field)
			}
			if b {
				return l, unsupported("SSH " + field)
			}
		}
	}
	l.host = strings.TrimSpace(str(p, "host"))
	l.host = strings.TrimPrefix(strings.TrimSuffix(l.host, "]"), "[")
	l.user = strings.TrimSpace(str(p, "user"))
	if l.host == "" || strings.ContainsAny(l.host, "/\\ \t\r\n\x00") {
		return l, errors.New("a valid SSH host is required")
	}
	if l.user == "" || strings.ContainsAny(l.user, "\r\n\x00") {
		return l, errors.New("an SSH user is required")
	}
	var err error
	l.port, err = sshNumber(p, "port", 22, 1, 65535)
	if err != nil {
		return l, err
	}
	timeout, err := sshNumber(p, "connect_timeout_secs", 15, 1, 300)
	if err != nil {
		return l, err
	}
	l.timeout = time.Duration(timeout) * time.Second
	l.password, l.keyPath, l.keyPassphrase = str(p, "password"), str(p, "key_path"), str(p, "key_passphrase")
	l.agentSocket = str(p, "ssh_agent_sock_path")
	l.authMethod = str(p, "auth_method")
	if l.authMethod == "" {
		switch {
		case l.keyPath != "" && l.password != "":
			l.authMethod = "key+password"
		case l.keyPath != "":
			l.authMethod = "key"
		case l.password != "":
			l.authMethod = "password"
		case boolean(p, "use_ssh_agent") || l.agentSocket != "":
			l.authMethod = "agent"
		default:
			l.authMethod = "none"
		}
	}
	switch l.authMethod {
	case "key", "key+password":
		if strings.TrimSpace(l.keyPath) == "" {
			return l, errors.New("selected SSH key authentication requires key_path")
		}
		if l.authMethod == "key+password" && l.password == "" {
			return l, errors.New("selected SSH key+password authentication requires a password")
		}
	case "password":
		if l.password == "" {
			return l, errors.New("selected SSH password authentication requires a password")
		}
	case "agent", "none":
	default:
		return l, fmt.Errorf("unsupported SSH authentication method %q", l.authMethod)
	}
	l.knownHosts = strings.TrimSpace(str(p, "known_hosts_path"))
	l.fingerprint = strings.TrimSpace(str(p, "host_key_fingerprint"))
	if l.fingerprint != "" {
		if !strings.HasPrefix(l.fingerprint, "SHA256:") {
			return l, errors.New("SSH host_key_fingerprint must be a SHA256 fingerprint")
		}
		digest, decodeErr := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(l.fingerprint, "SHA256:"))
		if decodeErr != nil || len(digest) != 32 {
			return l, errors.New("SSH host_key_fingerprint must contain a valid SHA256 digest")
		}
	}
	return l, nil
}

func sshNumber(p map[string]any, field string, fallback, min, max int) (int, error) {
	value := str(p, field)
	if value == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < min || n > max {
		return 0, fmt.Errorf("%s must be between %d and %d", field, min, max)
	}
	return n, nil
}

func expandSSHPath(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve SSH home directory: %w", err)
		}
		if path == "~" {
			return home, nil
		}
		return filepath.Join(home, strings.TrimPrefix(path, "~/")), nil
	}
	if strings.HasPrefix(path, "~") {
		return "", errors.New("SSH paths only support the current user's ~/ prefix")
	}
	return path, nil
}

func (l sshLayer) hostKeyCallback() (ssh.HostKeyCallback, error) {
	if l.fingerprint != "" {
		return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
			actual := ssh.FingerprintSHA256(key)
			if subtle.ConstantTimeCompare([]byte(actual), []byte(l.fingerprint)) == 1 {
				return nil
			}
			return fmt.Errorf("SSH host key mismatch for %s: received %s, expected %s; verify the server key with its administrator", hostname, actual, l.fingerprint)
		}, nil
	}
	path := l.knownHosts
	if path == "" {
		path = "~/.ssh/known_hosts"
	}
	path, err := expandSSHPath(path)
	if err != nil {
		return nil, err
	}
	if err := checkSSHRegularFile(path, 4<<20); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read SSH known_hosts: %w", err)
	}
	callback, err := knownhosts.New(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read SSH known_hosts: %w", err)
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if callback != nil {
			if verifyErr := callback(hostname, remote, key); verifyErr == nil {
				return nil
			}
		}
		return fmt.Errorf("SSH host key is unknown, changed, or revoked for %s (received %s); verify it with the server administrator, then update %s or set host_key_fingerprint", hostname, ssh.FingerprintSHA256(key), path)
	}, nil
}

// Refuse devices, pipes and oversized files before opening. In particular, a
// FIFO must not block an SSH connection before its context can cancel it.
func checkSSHRegularFile(path string, maxBytes int64) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("SSH configuration must be a regular file")
	}
	if info.Size() > maxBytes {
		return fmt.Errorf("SSH configuration file exceeds %d bytes", maxBytes)
	}
	return nil
}

func (l sshLayer) auth(ctx context.Context) ([]ssh.AuthMethod, func(), error) {
	cleanup := func() {}
	var methods []ssh.AuthMethod
	switch l.authMethod {
	case "none":
	case "password":
		methods = append(methods, ssh.Password(l.password))
	case "key", "key+password":
		path, err := expandSSHPath(l.keyPath)
		if err != nil {
			return nil, cleanup, err
		}
		if err := checkSSHRegularFile(path, 1<<20); err != nil {
			return nil, cleanup, fmt.Errorf("read SSH private key: %w", err)
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, cleanup, fmt.Errorf("read SSH private key: %w", err)
		}
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			file.Close()
			return nil, cleanup, errors.New("SSH private key must be a regular file")
		}
		key, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
		file.Close()
		if err != nil {
			return nil, cleanup, fmt.Errorf("read SSH private key: %w", err)
		}
		if len(key) > 1<<20 {
			return nil, cleanup, errors.New("SSH private key exceeds 1 MiB")
		}
		var signer ssh.Signer
		if l.keyPassphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(key, []byte(l.keyPassphrase))
		} else {
			signer, err = ssh.ParsePrivateKey(key)
		}
		if err != nil {
			return nil, cleanup, fmt.Errorf("parse SSH private key (check key format and passphrase): %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
		if l.authMethod == "key+password" {
			methods = append(methods, ssh.Password(l.password))
		}
	case "agent":
		path := l.agentSocket
		if path == "" {
			path = os.Getenv("SSH_AUTH_SOCK")
		}
		if path == "" {
			return nil, cleanup, errors.New("SSH agent authentication requires SSH_AUTH_SOCK or ssh_agent_sock_path")
		}
		path, err := expandSSHPath(path)
		if err != nil {
			return nil, cleanup, err
		}
		conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
		if err != nil {
			return nil, cleanup, fmt.Errorf("connect to SSH agent: %w", err)
		}
		stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
		cleanup = func() { stop(); _ = conn.Close() }
		signers, err := agent.NewClient(conn).Signers()
		if err != nil {
			cleanup()
			return nil, func() {}, fmt.Errorf("read SSH agent identities: %w", err)
		}
		if len(signers) == 0 {
			cleanup()
			return nil, func() {}, errors.New("SSH agent has no identities")
		}
		methods = append(methods, ssh.PublicKeys(signers...))
	}
	return methods, cleanup, nil
}

// sshChain owns only connections created for this database socket. Closing a
// socket cannot interrupt another pool connection or a different SSH tunnel.
type sshChain struct {
	mu      sync.Mutex
	closed  bool
	closers []io.Closer
}

func (s *sshChain) add(c io.Closer) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = c.Close()
		return
	}
	s.closers = append(s.closers, c)
	s.mu.Unlock()
}
func (s *sshChain) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	closers := s.closers
	s.closers = nil
	s.mu.Unlock()
	for i := len(closers) - 1; i >= 0; i-- {
		_ = closers[i].Close()
	}
	return nil
}

type sshOwnedConn struct {
	net.Conn
	chain *sshChain
}

func (c *sshOwnedConn) Close() error { return c.chain.Close() }

func dialSSHChain(ctx context.Context, layers []sshLayer) (*ssh.Client, *sshChain, error) {
	chain := &sshChain{}
	stop := context.AfterFunc(ctx, func() { _ = chain.Close() })
	defer stop()
	var client *ssh.Client
	for i, layer := range layers {
		next, err := dialSSHHop(ctx, layer, client, chain)
		if err != nil {
			_ = chain.Close()
			return nil, nil, fmt.Errorf("SSH hop %d: %w", i+1, err)
		}
		client = next
	}
	if err := ctx.Err(); err != nil {
		_ = chain.Close()
		return nil, nil, err
	}
	return client, chain, nil
}

func dialSSHHop(parent context.Context, layer sshLayer, previous *ssh.Client, chain *sshChain) (*ssh.Client, error) {
	ctx, cancel := context.WithTimeout(parent, layer.timeout)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { _ = chain.Close() })
	defer stop()
	callback, err := layer.hostKeyCallback()
	if err != nil {
		return nil, err
	}
	methods, cleanup, err := layer.auth(ctx)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	address := net.JoinHostPort(layer.host, strconv.Itoa(layer.port))
	var raw net.Conn
	if previous == nil {
		raw, err = (&net.Dialer{}).DialContext(ctx, "tcp", address)
	} else {
		raw, err = previous.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return nil, fmt.Errorf("connect to SSH server: %w", err)
	}
	chain.add(raw)
	conn, channels, requests, err := ssh.NewClientConn(raw, address, &ssh.ClientConfig{User: layer.user, Auth: methods, HostKeyCallback: callback, Timeout: layer.timeout})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("SSH handshake: %w", err)
	}
	client := ssh.NewClient(conn, channels, requests)
	chain.add(client)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return client, nil
}

// networkDialer returns a context-aware socket dialer. Every database socket
// receives its own SSH chain; no subprocesses or local forwarding listeners are
// created. The caller closes the entire chain by closing the returned socket.
func networkDialer(c Config) (func(context.Context, string, string) (net.Conn, error), error) {
	if err := ValidateNetworkTransports(c); err != nil {
		return nil, err
	}
	layers, err := parseSSHLayers(c.Transports)
	if err != nil {
		return nil, err
	}
	if len(layers) == 0 {
		return (&net.Dialer{}).DialContext, nil
	}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" && network != "tcp4" && network != "tcp6" {
			return nil, errors.New("SSH database forwarding requires a TCP target")
		}
		client, chain, err := dialSSHChain(ctx, layers)
		if err != nil {
			return nil, err
		}
		stop := context.AfterFunc(ctx, func() { _ = chain.Close() })
		defer stop()
		conn, err := client.DialContext(ctx, network, address)
		if err != nil {
			_ = chain.Close()
			return nil, fmt.Errorf("SSH database forwarding: %w", err)
		}
		chain.add(conn)
		if err := ctx.Err(); err != nil {
			_ = chain.Close()
			return nil, err
		}
		// SSH channels do not implement deadlines. An in-memory net.Pipe
		// supplies normal, resettable net.Conn deadlines to database drivers
		// while these pumps preserve streaming backpressure. No listening
		// socket or additional network connection is created.
		local, relay := net.Pipe()
		chain.add(local)
		chain.add(relay)
		go func() { _, _ = io.Copy(relay, conn); _ = chain.Close() }()
		go func() { _, _ = io.Copy(conn, relay); _ = chain.Close() }()
		return &sshOwnedConn{Conn: local, chain: chain}, nil
	}, nil
}

// TestSSHTunnel verifies the server host key and selected authentication method
// without connecting to a database or creating a persistent forwarding port.
func TestSSHTunnel(ctx context.Context, layer map[string]any) (string, error) {
	parsed, err := parseSSHLayer(layer)
	if err != nil {
		return "", err
	}
	_, chain, err := dialSSHChain(ctx, []sshLayer{parsed})
	if err != nil {
		return "", err
	}
	_ = chain.Close()
	return "SSH host key verified and authentication successful", nil
}

// TestSSHTransports verifies each enabled SSH hop, in order, and closes the
// chain immediately. It intentionally does not probe the database target.
func TestSSHTransports(ctx context.Context, c Config) (string, error) {
	if err := ValidateNetworkTransports(c); err != nil {
		return "", err
	}
	layers, err := parseSSHLayers(c.Transports)
	if err != nil {
		return "", err
	}
	if len(layers) == 0 {
		return "", errors.New("no enabled SSH transport layers")
	}
	_, chain, err := dialSSHChain(ctx, layers)
	if err != nil {
		return "", err
	}
	_ = chain.Close()
	return fmt.Sprintf("SSH host keys verified and authentication successful for %d hop(s)", len(layers)), nil
}
