package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxTransferBytes = 32 << 20
const maxTransferRows = 100000

type importSource struct {
	columns []string
	rows    [][]any
	created time.Time
}
type transferJob struct {
	mu                    sync.Mutex
	changed               chan struct{}
	progress              map[string]any
	started, done         bool
	created               time.Time
	cancel                context.CancelFunc
	data                  []byte
	filename, contentType string
}

func (j *transferJob) update(p map[string]any, done bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for k, v := range p {
		j.progress[k] = v
	}
	j.done = done
	close(j.changed)
	j.changed = make(chan struct{})
}

type extensionState struct {
	mu      sync.Mutex
	jobs    map[string]*transferJob
	sources map[string]*importSource
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

func newExtensionState() *extensionState {
	ctx, cancel := context.WithCancel(context.Background())
	return &extensionState{jobs: map[string]*transferJob{}, sources: map[string]*importSource{}, ctx: ctx, cancel: cancel}
}
func (e *extensionState) close() {
	e.mu.Lock()
	e.cancel()
	e.mu.Unlock()
	e.wg.Wait()
}
func (e *extensionState) start(work func()) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ctx.Err() != nil {
		return false
	}
	e.wg.Add(1)
	go func() { defer e.wg.Done(); work() }()
	return true
}
func (e *extensionState) job(key, id, kind string) (*transferJob, error) {
	if id == "" || len(id) > 128 || strings.ContainsAny(id, "/\\\x00") {
		return nil, errors.New("Invalid transfer ID")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for k, j := range e.jobs {
		j.mu.Lock()
		expired := time.Since(j.created) > 30*time.Minute && (!j.started || j.done)
		j.mu.Unlock()
		if expired {
			delete(e.jobs, k)
		}
	}
	if j := e.jobs[key]; j != nil {
		return j, nil
	}
	if len(e.jobs) >= 32 {
		return nil, errors.New("Too many transfer jobs; retry after older jobs expire")
	}
	j := &transferJob{changed: make(chan struct{}), created: time.Now(), progress: map[string]any{kind + "Id": id, "status": "running", "rowsImported": 0, "rowsExported": 0}}
	e.jobs[key] = j
	return j, nil
}
func transferID() string { var b [16]byte; _, _ = rand.Read(b[:]); return hex.EncodeToString(b[:]) }
func mapValue(p map[string]any, k string) map[string]any {
	v, _ := p[k].(map[string]any)
	if v == nil {
		return map[string]any{}
	}
	return v
}
func boolValue(p map[string]any, k string) bool { v, _ := p[k].(bool); return v }
func numberValue(p map[string]any, k string, def int) int {
	switch v := p[k].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	case string:
		n, e := strconv.Atoi(v)
		if e == nil {
			return n
		}
	}
	return def
}
func (s *Server) streamJob(w http.ResponseWriter, r *http.Request, j *transferJob) {
	if r.URL.Query().Get("poll") == "1" {
		j.mu.Lock()
		defer j.mu.Unlock()
		writeJSON(w, 200, j.progress)
		return
	}
	f, ok := w.(http.Flusher)
	if !ok {
		writeError(w, 501, "Use progress polling in the desktop runtime")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	_, _ = io.WriteString(w, ": connected\n\n")
	f.Flush()
	timer := time.NewTicker(15 * time.Second)
	defer timer.Stop()
	for {
		j.mu.Lock()
		data, _ := json.Marshal(j.progress)
		done := j.done
		changed := j.changed
		j.mu.Unlock()
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return
		}
		f.Flush()
		if done {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-s.extensions.ctx.Done():
			return
		case <-changed:
		case <-timer.C:
		}
	}
}
