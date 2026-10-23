package server

import (
	"bytes"
	"crypto/cipher"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStateCloneMatchesJSONAndIsDetached(t *testing.T) {
	state := emptyState()
	state.Connections = []map[string]any{{"id": "connection", "layers": []map[string]any{{"host": "before"}}}}
	state.Settings["values"] = map[string]any{
		"integer": int64(1700000000123), "fraction": float32(0.1), "number": json.Number("1.25"),
		"typed": []string{"before"}, "empty": []any{}, "null": []any(nil),
		"nested": []any{map[string]any{"name": "before"}},
		"struct": struct{ Name string }{Name: "before"},
	}
	state.Sidebar = map[string]any{"children": []any{map[string]any{"name": "before"}}}
	state.TableGroups["group"] = []any{"before"}
	state.History = []map[string]any{{"sql": "before"}}
	state.SQLFiles = []map[string]any{{"sql": "before"}}
	state.SQLFolders = []map[string]any{{"name": "before"}}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var expected diskState
	if err := json.Unmarshal(encoded, &expected); err != nil {
		t.Fatal(err)
	}
	copy, err := cloneDiskState(state)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(copy, expected) {
		t.Fatalf("clone changed JSON representation: got %#v, want %#v", copy, expected)
	}
	copy.Connections[0]["layers"].([]any)[0].(map[string]any)["host"] = "after"
	values := copy.Settings["values"].(map[string]any)
	values["typed"].([]any)[0] = "after"
	values["nested"].([]any)[0].(map[string]any)["name"] = "after"
	copy.Sidebar.(map[string]any)["children"].([]any)[0].(map[string]any)["name"] = "after"
	copy.TableGroups["group"].([]any)[0] = "after"
	copy.History[0]["sql"] = "after"
	copy.SQLFiles[0]["sql"] = "after"
	copy.SQLFolders[0]["name"] = "after"
	after, err := json.Marshal(state)
	if err != nil || !bytes.Equal(encoded, after) {
		t.Fatal("mutating a snapshot changed its source state", err)
	}
}

func TestStateUpdateFailureLeavesLiveAndDiskUnchanged(t *testing.T) {
	dir := t.TempDir()
	s := testServer(t, dir)
	if err := s.update(func(st *diskState) error {
		st.Settings["nested"] = map[string]any{"items": []any{map[string]any{"value": "before"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before := s.snapshot()
	path := filepath.Join(dir, "state.enc")
	beforeBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"callback", "encoding", "publish"} {
		t.Run(stage, func(t *testing.T) {
			if stage == "publish" {
				backup := filepath.Join(dir, "state.backup")
				if err := os.Rename(path, backup); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					_ = os.Remove(path)
					if err := os.Rename(backup, path); err != nil {
						t.Error(err)
					}
				})
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			err := s.update(func(st *diskState) error {
				st.Settings["nested"].(map[string]any)["items"].([]any)[0].(map[string]any)["value"] = "after"
				if stage == "callback" {
					return errors.New("reject update")
				}
				if stage == "encoding" {
					st.Settings["invalid"] = make(chan struct{})
				}
				return nil
			})
			if err == nil {
				t.Fatal("invalid update succeeded")
			}
			if !reflect.DeepEqual(s.snapshot(), before) {
				t.Fatal("failed update changed live state")
			}
		})
		afterBytes, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(beforeBytes, afterBytes) {
			t.Fatal("failed update changed saved state", err)
		}
	}
	temporary, err := filepath.Glob(filepath.Join(dir, ".state-*"))
	if err != nil || len(temporary) != 0 {
		t.Fatal("failed updates left temporary files", temporary, err)
	}
}

type blockingStoreAEAD struct {
	cipher.AEAD
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (a *blockingStoreAEAD) Seal(dst, nonce, plaintext, additionalData []byte) []byte {
	a.once.Do(func() { close(a.entered) })
	<-a.release
	return a.AEAD.Seal(dst, nonce, plaintext, additionalData)
}

func TestStateReadersProceedDuringSaveAndCacheRecencySurvives(t *testing.T) {
	dir := t.TempDir()
	s := testServer(t, dir)
	if err := s.update(func(st *diskState) error {
		st.Settings["tab.cache"] = map[string]any{"cached": map[string]any{"payloadBase64": "eA==", "lastAccessedAt": 1, "createdAt": 1}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	barrier := &blockingStoreAEAD{AEAD: s.aead, entered: make(chan struct{}), release: make(chan struct{})}
	s.aead = barrier
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(barrier.release) }) }
	defer release()
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- s.update(func(st *diskState) error { st.Settings["saved"] = true; return nil })
	}()
	select {
	case <-barrier.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("write did not reach persistence")
	}
	readDone := make(chan error, 1)
	go func() {
		if s.snapshot().Settings["saved"] != nil {
			readDone <- errors.New("uncommitted update became visible")
			return
		}
		entry := s.tabCacheEntry("cached")
		if cacheAccessTime(entry["lastAccessedAt"]) <= 1 {
			readDone <- errors.New("cache read did not update recency")
			return
		}
		if w := call(s, "POST", "/api/auth/login", map[string]any{"password": "test-auth-password"}, nil, ""); w.Code != 200 {
			readDone <- fmt.Errorf("login failed: %d", w.Code)
			return
		}
		readDone <- nil
	}()
	select {
	case err := <-readDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("state reads or authentication waited for disk persistence")
	}
	access := cacheAccessTime(s.tabCacheMetadata()[0]["lastAccessedAt"])
	release()
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	if got := cacheAccessTime(s.tabCacheMetadata()[0]["lastAccessedAt"]); got != access {
		t.Fatalf("publishing an update lost concurrent cache recency: got %d, want %d", got, access)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	again := testServer(t, dir)
	if again.snapshot().Settings["saved"] != true {
		t.Fatal("durable update missing after restart")
	}
	if got := cacheAccessTime(again.tabCacheMetadata()[0]["lastAccessedAt"]); got != access {
		t.Fatalf("Close did not persist newer cache recency: got %d, want %d", got, access)
	}
}

func TestStateConcurrentReadersAndUpdates(t *testing.T) {
	s := testServer(t, t.TempDir())
	if err := s.update(func(st *diskState) error {
		st.Settings["tab.cache"] = map[string]any{"cached": map[string]any{"payloadBase64": "eA=="}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for reader := 0; reader < 4; reader++ {
		wg.Go(func() {
			for range 32 {
				s.tabCacheEntry("cached")["payloadBase64"] = "isolated"
				s.tabCacheMetadata()[0]["key"] = "isolated"
				_ = s.snapshot()
				_ = s.redactError("test", nil)
			}
		})
	}
	for writer := 0; writer < 2; writer++ {
		wg.Go(func() {
			for range 6 {
				if err := s.update(func(st *diskState) error {
					st.Settings["counter"] = integer(st.Settings["counter"], 0) + 1
					return nil
				}); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	if integer(s.snapshot().Settings["counter"], 0) != 12 {
		t.Fatal("concurrent updates were lost")
	}
	if s.tabCacheEntry("cached")["payloadBase64"] != "eA==" {
		t.Fatal("cache response exposed live state")
	}
}

func BenchmarkStateSnapshot(b *testing.B) {
	for _, size := range []int{1024, 32 << 20} {
		b.Run(fmt.Sprintf("state_%d", size), func(b *testing.B) {
			s := &Server{state: emptyState()}
			s.state.Settings["synthetic-cache"] = strings.Repeat("x", size)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = s.snapshot().Connections
			}
		})
	}
}

func BenchmarkTabCacheHit(b *testing.B) {
	for _, size := range []int{1024, 32 << 20} {
		b.Run(fmt.Sprintf("state_%d", size), func(b *testing.B) {
			s, err := New(Options{DataDir: b.TempDir(), Desktop: true, DisableMCPListener: true})
			if err != nil {
				b.Fatal(err)
			}
			defer s.Close()
			if err := s.update(func(st *diskState) error {
				st.Settings["synthetic-cache"] = strings.Repeat("x", size)
				st.Settings["tab.cache"] = map[string]any{"key": map[string]any{"payloadBase64": "eA==", "byteSize": 1}}
				return nil
			}); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				w := httptest.NewRecorder()
				r := httptest.NewRequest("GET", "/api/tab-runtime-cache", nil)
				s.handleAppCache(w, r, map[string]any{"key": "key"})
				if w.Code != 200 {
					b.Fatal(w.Code)
				}
			}
			b.StopTimer()
		})
	}
}
