//go:build desktop

package main

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

func TestDesktopAPIOnlyNormalizesNativeWebViewSentinel(t *testing.T) {
	for _, remote := range []string{"192.0.2.1:1234", "127.0.0.1:34115", "10.0.0.10:3456"} {
		t.Run(remote, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "http://wails/api/auth/check", nil)
			request.RemoteAddr = remote
			var observed string
			handler := desktopAPI(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { observed = r.RemoteAddr }))
			handler.ServeHTTP(httptest.NewRecorder(), request)
			expected := remote
			if remote == "192.0.2.1:1234" {
				expected = ""
			}
			if observed != expected {
				t.Fatalf("RemoteAddr = %q, want %q", observed, expected)
			}
			if request.RemoteAddr != remote {
				t.Fatal("mutated upstream request")
			}
		})
	}
}

func TestSaveFileBytesWritesBinaryAndCancellationCreatesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.xlsx")
	app := &App{saveDialog: func(runtime.SaveDialogOptions) (string, error) { return path, nil }}
	content := []byte{0, 1, 127, 255}
	saved, err := app.SaveFileBytes("result.xlsx", base64.StdEncoding.EncodeToString(content))
	if err != nil || saved != path {
		t.Fatalf("save: %q %v", saved, err)
	}
	actual, err := os.ReadFile(path)
	if err != nil || string(actual) != string(content) {
		t.Fatalf("contents: %v %v", actual, err)
	}
	app.saveDialog = func(runtime.SaveDialogOptions) (string, error) { return "", nil }
	if saved, err = app.SaveFileBytes("cancelled.csv", "aWdub3JlZA=="); err != nil || saved != "" {
		t.Fatalf("cancel: %q %v", saved, err)
	}
	if _, err = app.SaveFileBytes("bad.csv", "invalid base64"); err == nil {
		t.Fatal("invalid base64 succeeded")
	}
}

func TestSaveExportPreservesDestinationOnBackendError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "result.csv")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	app := &App{
		saveDialog: func(runtime.SaveDialogOptions) (string, error) { return path, nil },
		backend:    http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "failed", 500) }),
	}
	if _, err := app.SaveQueryResultExport("job", "result.csv"); err == nil {
		t.Fatal("failed download reported success")
	}
	actual, _ := os.ReadFile(path)
	if string(actual) != "original" {
		t.Fatal("failed download overwrote original file")
	}
	app.backend = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.RemoteAddr != "" {
			t.Error("internal download unexpectedly has a network address")
		}
		w.Write([]byte("id,name\n1,test\n"))
	})
	if saved, err := app.SaveQueryResultExport("job", "result.csv"); err != nil || saved != path {
		t.Fatalf("download: %q %v", saved, err)
	}
	actual, _ = os.ReadFile(path)
	if string(actual) != "id,name\n1,test\n" {
		t.Fatalf("downloaded contents: %s", actual)
	}
}

func TestSaveFileDialogKeepsOptionsAndCancellation(t *testing.T) {
	app := &App{saveDialog: func(options runtime.SaveDialogOptions) (string, error) {
		if options.DefaultFilename != "new.db" || options.Filters[0].Pattern != "*.db;*.sqlite" {
			t.Errorf("unexpected options: %+v", options)
		}
		return "", nil
	}}
	path, err := app.SaveFile(FileDialogOptions{DefaultPath: "new.db", Filters: []FileFilter{{Name: "SQLite", Extensions: []string{"db", "sqlite"}}}})
	if path != "" || err != nil {
		t.Fatalf("cancel: %q %v", path, err)
	}
}
