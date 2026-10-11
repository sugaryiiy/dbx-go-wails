package server

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const maxPluginJSON = 8 << 20
const maxPluginBinary = 64 << 20

type pluginReply struct {
	value any
	err   error
}
type pluginProcess struct {
	id       string
	manifest map[string]any
	cmd      *exec.Cmd
	in       io.WriteCloser
	framed   bool
	mu       sync.Mutex
	writeMu  sync.Mutex
	pending  map[uint64]chan pluginReply
	failure  error
	next     atomic.Uint64
	done     chan struct{}
	stop     sync.Once
	owner    *pluginRuntime
	ctx      context.Context
	cancel   context.CancelFunc
}
type pluginRuntime struct {
	s        *Server
	mu       sync.Mutex
	opMu     sync.Mutex
	dataMu   sync.Mutex
	sessions map[string]*pluginProcess
	closed   bool
	events   []map[string]any
	seq      uint64
	wake     chan struct{}
	dataOps  map[string]map[uint64]context.CancelFunc
	dataSeq  uint64
}

func newPluginRuntime(s *Server) *pluginRuntime {
	return &pluginRuntime{s: s, sessions: map[string]*pluginProcess{}, events: []map[string]any{}, wake: make(chan struct{}), dataOps: map[string]map[uint64]context.CancelFunc{}}
}
func (s *Server) closePlugins() {
	if s.pluginRuntime == nil {
		return
	}
	p := s.pluginRuntime
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	for _, scope := range p.dataOps {
		for _, cancel := range scope {
			cancel()
		}
	}
	sessions := []*pluginProcess{}
	for _, session := range p.sessions {
		sessions = append(sessions, session)
	}
	p.sessions = map[string]*pluginProcess{}
	close(p.wake)
	p.mu.Unlock()
	for _, session := range sessions {
		session.close()
	}
	// An activation can be between starting its process and registering it.
	// Marking closed first makes that activation stop before its handshake.
	p.opMu.Lock()
	p.opMu.Unlock()
}
func (p *pluginRuntime) emit(event map[string]any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.seq++
	event = cloneMap(event)
	event["seq"] = float64(p.seq)
	p.events = append(p.events, event)
	if len(p.events) > 512 {
		p.events = p.events[len(p.events)-512:]
	}
	close(p.wake)
	p.wake = make(chan struct{})
}
func (p *pluginProcess) fail(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failure != nil {
		return
	}
	p.failure = err
	if p.cancel != nil {
		p.cancel()
	}
	for id, ch := range p.pending {
		ch <- pluginReply{err: err}
		delete(p.pending, id)
	}
}
func (p *pluginProcess) close() {
	p.stop.Do(func() {
		p.fail(errors.New("Plugin runtime stopped"))
		p.in.Close()
		if p.cmd != nil && p.cmd.Process != nil {
			p.cmd.Process.Kill()
		}
	})
	select {
	case <-p.done:
	case <-time.After(3 * time.Second):
	}
}
func (p *pluginProcess) running() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.failure == nil }
func (p *pluginProcess) writeFrameRaw(ctx context.Context, kind byte, b []byte) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.framed {
		header := [5]byte{kind}
		binary.BigEndian.PutUint32(header[1:], uint32(len(b)))
		if _, err := p.in.Write(header[:]); err != nil {
			return err
		}
		_, err := p.in.Write(b)
		return err
	}
	if kind != 0 {
		return errors.New("Binary requires framed plugin transport")
	}
	_, err := p.in.Write(append(b, '\n'))
	return err
}
func (p *pluginProcess) writeFrame(ctx context.Context, kind byte, b []byte) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	written := make(chan error, 1)
	go func() { written <- p.writeFrameRaw(ctx, kind, b) }()
	select {
	case err := <-written:
		if err != nil {
			p.close()
		}
		return err
	case <-ctx.Done():
		p.close()
		return ctx.Err()
	}
}
func (p *pluginProcess) write(ctx context.Context, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(b) > maxPluginJSON {
		return errors.New("Plugin request exceeds 8 MiB")
	}
	return p.writeFrame(ctx, 0, b)
}
func pluginMethod(method string) bool {
	return method != "" && len(method) <= 256 && !strings.ContainsAny(method, "\r\n\x00")
}
func (p *pluginProcess) invoke(ctx context.Context, method string, params any) (any, error) {
	if !pluginMethod(method) || strings.HasPrefix(method, "host/") {
		return nil, errors.New("Invalid or reserved plugin method")
	}
	id := p.next.Add(1)
	ch := make(chan pluginReply, 1)
	p.mu.Lock()
	if p.failure != nil {
		err := p.failure
		p.mu.Unlock()
		return nil, err
	}
	p.pending[id] = ch
	p.mu.Unlock()
	defer func() { p.mu.Lock(); delete(p.pending, id); p.mu.Unlock() }()
	if err := p.write(ctx, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	select {
	case result := <-ch:
		return result.value, result.err
	case <-ctx.Done():
		p.close()
		return nil, ctx.Err()
	}
}
func (p *pluginProcess) receiveJSON(b []byte) error {
	var message map[string]any
	if json.Unmarshal(b, &message) != nil || str(message, "jsonrpc") != "2.0" {
		return errors.New("Invalid plugin protocol message")
	}
	if method := str(message, "method"); method != "" {
		if _, exists := message["id"]; exists {
			go p.hostRequest(message)
		} else if pluginHasPermission(p.manifest, "host.events") {
			p.owner.emit(map[string]any{"kind": "event", "pluginId": p.id, "method": method, "params": message["params"]})
		}
		return nil
	}
	id := uint64(integer(message["id"], 0))
	if id == 0 {
		return errors.New("Invalid plugin response id")
	}
	p.mu.Lock()
	ch := p.pending[id]
	delete(p.pending, id)
	p.mu.Unlock()
	if ch != nil {
		var err error
		if failure, ok := message["error"].(map[string]any); ok {
			err = errors.New("Plugin RPC error: " + str(failure, "message"))
		} else if _, ok := message["result"]; !ok {
			err = errors.New("Plugin RPC response has no result")
		}
		ch <- pluginReply{value: message["result"], err: err}
	}
	return nil
}
func (p *pluginProcess) read(output io.Reader) {
	defer func() {
		p.fail(errors.New("Plugin protocol stream closed"))
		if p.cmd.Process != nil {
			p.cmd.Process.Kill()
		}
	}()
	if !p.framed {
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 64<<10), maxPluginBinary)
		for scanner.Scan() {
			if err := p.receiveJSON(scanner.Bytes()); err != nil {
				p.fail(err)
				return
			}
		}
		return
	}
	for {
		header := [5]byte{}
		if _, err := io.ReadFull(output, header[:]); err != nil {
			return
		}
		size := int(binary.BigEndian.Uint32(header[1:]))
		max := maxPluginJSON
		if header[0] == 1 {
			max = maxPluginBinary + 65537
		} else if header[0] != 0 {
			p.fail(errors.New("Unknown plugin frame"))
			return
		}
		if size > max {
			p.fail(errors.New("Plugin frame exceeds size limit"))
			return
		}
		b := make([]byte, size)
		if _, err := io.ReadFull(output, b); err != nil {
			return
		}
		if header[0] == 0 {
			if err := p.receiveJSON(b); err != nil {
				p.fail(err)
				return
			}
		} else {
			if len(b) < 2 {
				return
			}
			length := int(binary.BigEndian.Uint16(b[:2]))
			if len(b) < 2+length {
				return
			}
			if pluginHasPermission(p.manifest, "host.binary") {
				p.owner.emit(map[string]any{"kind": "binary", "pluginId": p.id, "channel": string(b[2 : 2+length]), "dataBase64": base64.StdEncoding.EncodeToString(b[2+length:])})
			}
		}
	}
}
func pluginHasPermission(manifest map[string]any, permission string) bool {
	if values, ok := manifest["permissions"].([]any); ok {
		for _, v := range values {
			if v == permission {
				return true
			}
		}
	}
	return false
}
func (p *pluginRuntime) activate(ctx context.Context, id string) (*pluginProcess, error) {
	p.opMu.Lock()
	defer p.opMu.Unlock()
	return p.activateLocked(ctx, id)
}
func (p *pluginRuntime) activateLocked(ctx context.Context, id string) (*pluginProcess, error) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, errors.New("Plugin runtime closed")
	}
	if session := p.sessions[id]; session != nil && session.running() {
		p.mu.Unlock()
		return session, nil
	}
	previous := p.sessions[id]
	p.mu.Unlock()
	if previous != nil {
		previous.close()
	}
	record, err := p.s.pluginRecord(id)
	if err != nil {
		return nil, err
	}
	manifest := appObject(record["manifest"])
	root := p.s.pluginDir(record)
	backend := appObject(appObject(manifest["entrypoints"])["backend"])
	if len(backend) == 0 {
		return nil, errors.New("Plugin has no backend entrypoint")
	}
	path, err := safePluginFile(root, str(backend, "executable"))
	if err != nil {
		return nil, err
	}
	dataDir := filepath.Join(p.s.dataDir, "plugin-data", id)
	if err = os.MkdirAll(dataDir, 0700); err != nil {
		return nil, errors.New("Cannot create plugin data directory")
	}
	cmd := exec.Command(path)
	cmd.Dir = root
	cmd.Env = []string{}
	for _, key := range []string{"PATH", "HOME", "USERPROFILE", "SYSTEMROOT", "TMPDIR", "TEMP"} {
		if value, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	cmd.Env = append(cmd.Env, "DBX_PLUGIN_DATA_DIR="+dataDir)
	cmd.Stderr = io.Discard
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		in.Close()
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		in.Close()
		return nil, errors.New("Cannot launch plugin backend")
	}
	processCtx, stopProcess := context.WithCancel(context.Background())
	session := &pluginProcess{id: id, manifest: manifest, cmd: cmd, in: in, framed: str(backend, "transport") == "stdio-framed", pending: map[uint64]chan pluginReply{}, done: make(chan struct{}), owner: p, ctx: processCtx, cancel: stopProcess}
	go session.read(out)
	go func() {
		cmd.Wait()
		session.fail(errors.New("Plugin backend exited"))
		close(session.done)
		p.emit(map[string]any{"kind": "event", "pluginId": id, "method": "dbx/runtime/status", "params": map[string]any{"state": "exited"}})
	}()
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		session.close()
		return nil, errors.New("Plugin runtime closed")
	}
	p.sessions[id] = session
	p.mu.Unlock()
	handshakeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	reply, err := session.invoke(handshakeCtx, "plugin/initialize", map[string]any{"host": map[string]any{"dbxVersion": pluginAppVersion, "hostApiVersion": "1.4.0", "protocolVersions": []int{1}, "features": []string{}}, "plugin": map[string]any{"id": id, "version": manifest["version"]}, "permissions": manifest["permissions"]})
	if err != nil {
		session.close()
		return nil, err
	}
	hello := appObject(reply)
	identity := appObject(hello["plugin"])
	if integer(hello["protocolVersion"], 0) != 1 || str(identity, "id") != id || str(identity, "version") != str(manifest, "version") {
		session.close()
		return nil, errors.New("Plugin handshake identity or protocol does not match manifest")
	}
	return session, nil
}
func (p *pluginProcess) hostRequest(request map[string]any) {
	method := str(request, "method")
	base := p.ctx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithTimeout(base, 30*time.Second)
	defer cancel()
	var result any
	var err error
	params := appObject(request["params"])
	switch method {
	case "host/queryData", "host.queryData":
		result, err = p.owner.s.pluginDataQuery(ctx, p.id, params)
	case "host/getTableMetadata", "host.getTableMetadata":
		result, err = p.owner.s.pluginTableMetadata(ctx, p.id, params)
	default:
		err = errors.New("This plugin-initiated Host API method has not yet been migrated to Go")
	}
	response := map[string]any{"jsonrpc": "2.0", "id": request["id"]}
	if err != nil {
		response["error"] = map[string]any{"code": -32601, "message": err.Error()}
	} else {
		response["result"] = result
	}
	_ = p.write(ctx, response)
}
