package database

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type lifecycleNative struct {
	entered chan struct{}
	closed  atomic.Bool
}

func (d *lifecycleNative) Close() error                   { d.closed.Store(true); return nil }
func (d *lifecycleNative) Ping(ctx context.Context) error { return ctx.Err() }
func (d *lifecycleNative) Info(context.Context) (map[string]any, error) {
	return map[string]any{"productName": "Lifecycle fixture"}, nil
}
func (d *lifecycleNative) Handle(ctx context.Context, path string, p map[string]any, locked bool) (any, error) {
	if path == "/api/fixture/block" {
		close(d.entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return locked, nil
}
func fixtureNativeManager(t *testing.T) (*Manager, *lifecycleNative) {
	t.Helper()
	m := NewManager()
	driver := &lifecycleNative{entered: make(chan struct{})}
	life, cancel := context.WithCancel(context.Background())
	m.connections["native"] = &connection{config: Config{ID: "native", Type: "redis", ReadOnly: true}, native: driver, life: life, cancel: cancel}
	t.Cleanup(func() {
		if err := m.Close(); err != nil {
			t.Errorf("close database manager: %v", err)
		}
	})
	return m, driver
}
func TestNativeConnectionManagerLifecycle(t *testing.T) {
	for _, action := range []string{"cancel", "disconnect", "close", "replace"} {
		t.Run(action, func(t *testing.T) {
			var replacementPath string
			if action == "replace" {
				replacementPath = filepath.Join(t.TempDir(), "replacement.sqlite")
			}
			m, driver := fixtureNativeManager(t)
			done := make(chan error, 1)
			go func() {
				_, err := m.Handle(context.Background(), "/api/fixture/block", map[string]any{"connectionId": "native", "executionId": "native-op"})
				done <- err
			}()
			select {
			case <-driver.entered:
			case <-time.After(time.Second):
				t.Fatal("native query did not start")
			}
			var err error
			switch action {
			case "cancel":
				var result any
				result, err = m.Handle(context.Background(), "/api/query/cancel", map[string]any{"executionId": "native-op"})
				if result != true {
					t.Fatal("query not registered")
				}
			case "disconnect":
				_, err = m.Handle(context.Background(), "/api/connection/disconnect", map[string]any{"connectionId": "native"})
			case "close":
				err = m.Close()
			case "replace":
				err = m.Connect(context.Background(), Config{ID: "native", Type: "sqlite", Host: replacementPath})
			}
			if err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal("native query was not cancelled", err)
				}
			case <-time.After(time.Second):
				t.Fatal("native query outlived its connection")
			}
			if action != "cancel" && !driver.closed.Load() {
				t.Fatal("native driver not closed")
			}
		})
	}
}
func TestNativeConnectionWriteProtectionAndSQLBoundary(t *testing.T) {
	m, _ := fixtureNativeManager(t)
	p := map[string]any{"connectionId": "native"}
	result, err := m.Handle(context.Background(), "/api/fixture/locked", p)
	if err != nil || result != true {
		t.Fatal("native readonly flag missing")
	}
	if _, _, _, err = m.pool(context.Background(), "native", ""); !errors.Is(err, ErrUnsupported) {
		t.Fatal("non-SQL native driver entered SQL pool")
	}
	_, err = m.Handle(context.Background(), "/api/connection/write-unlock", map[string]any{"connectionId": "native", "durationSecs": 1})
	if err != nil {
		t.Fatal(err)
	}
	result, err = m.Handle(context.Background(), "/api/fixture/locked", p)
	if err != nil || result != false {
		t.Fatal("native explicit unlock not applied")
	}
	p["readOnly"] = true
	result, err = m.Handle(context.Background(), "/api/fixture/locked", p)
	if err != nil || result != true {
		t.Fatal("request readonly flag bypassed")
	}
	delete(p, "readOnly")
	_, err = m.Handle(context.Background(), "/api/connection/write-unlock/lock", p)
	if err != nil {
		t.Fatal(err)
	}
	result, err = m.Handle(context.Background(), "/api/fixture/locked", p)
	if err != nil || result != true {
		t.Fatal("native relock not applied")
	}
}
func TestNativeConfigRetainsEngineOptions(t *testing.T) {
	var c Config
	if err := json.Unmarshal([]byte(`{"id":"x","db_type":"redis","redis_connection_mode":"cluster","ssl":true,"transport_layers":[{"type":"ssh"}]}`), &c); err != nil {
		t.Fatal(err)
	}
	if c.Type != "redis" || !c.SSL || c.Raw["redis_connection_mode"] != "cluster" || len(c.Transports) != 1 {
		t.Fatal("engine options lost during decode")
	}
}
