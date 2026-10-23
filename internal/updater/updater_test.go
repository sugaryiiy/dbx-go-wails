package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeReleaseServer struct {
	*httptest.Server
	payload   []byte
	digest    string
	assetHits atomic.Int32
}

func newFakeReleaseServer(t *testing.T, payload []byte, releaseBody func(base string, digest string) string) *fakeReleaseServer {
	t.Helper()
	sum := sha256.Sum256(payload)
	fake := &fakeReleaseServer{payload: payload, digest: hex.EncodeToString(sum[:])}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/sugaryiiy/magpiedb/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	mux.HandleFunc("/repos/sugaryiiy/magpiedb/releases", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(releaseBody(fake.URL, fake.digest)))
	})
	mux.HandleFunc("/asset", func(w http.ResponseWriter, r *http.Request) {
		fake.assetHits.Add(1)
		_, _ = w.Write(payload)
	})
	fake.Server = httptest.NewServer(mux)
	t.Cleanup(fake.Close)
	return fake
}

func linuxReleaseJSON(base, digest, assetDigest string) string {
	asset := map[string]any{
		"name":                 "MagpieDB-0.6.34-Linux-x64.tar.gz",
		"browser_download_url": base + "/asset",
		"size":                 4096,
	}
	if assetDigest != "" {
		asset["digest"] = assetDigest
	}
	release := map[string]any{
		"tag_name":     "v0.6.34",
		"name":         "MagpieDB 0.6.34",
		"html_url":     "https://github.com/sugaryiiy/magpiedb/releases/tag/v0.6.34",
		"body":         "Release notes",
		"prerelease":   false,
		"draft":        false,
		"published_at": "2026-10-24T03:00:00Z",
		"assets":       []any{asset},
	}
	data, _ := json.Marshal([]any{release})
	return string(data)
}

func newTestService(t *testing.T, apiBase string) *Service {
	t.Helper()
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		t.Skip("fixture assets target linux/amd64")
	}
	return NewService(filepath.Join(t.TempDir(), "updates"), NewGitHubClient(apiBase, "0.6.33"))
}

func TestDownloadVerifiesAndCaches(t *testing.T) {
	payload := []byte("fake magpiedb linux package payload")
	fake := newFakeReleaseServer(t, payload, func(base, digest string) string {
		return linuxReleaseJSON(base, digest, "sha256:"+digest)
	})
	service := newTestService(t, fake.URL)
	var progress []DownloadProgress
	downloaded, err := service.Download(context.Background(), "0.6.34", "attempt-1", "", func(p DownloadProgress) {
		progress = append(progress, p)
	})
	if err != nil {
		t.Fatal(err)
	}
	if downloaded.Version != "0.6.34" || downloaded.CacheID != "attempt-1" || downloaded.ReleaseURL == "" {
		t.Fatalf("downloaded = %+v", downloaded)
	}
	assetPath := filepath.Join(service.Root(), "attempt-1", "MagpieDB-0.6.34-Linux-x64.tar.gz")
	content, err := os.ReadFile(assetPath)
	if err != nil || string(content) != string(payload) {
		t.Fatalf("cached asset = %q, err %v", content, err)
	}
	if len(progress) == 0 || progress[len(progress)-1].Downloaded != int64(len(payload)) {
		t.Fatalf("progress = %+v", progress)
	}
	restored, err := service.Downloaded()
	if err != nil || restored == nil || restored.CacheID != "attempt-1" {
		t.Fatalf("restored = %+v, err %v", restored, err)
	}
	if err := service.Discard("attempt-1"); err != nil {
		t.Fatal(err)
	}
	restored, err = service.Downloaded()
	if err != nil || restored != nil {
		t.Fatalf("after discard restored = %+v, err %v", restored, err)
	}
}

func TestDownloadRejectsChecksumMismatch(t *testing.T) {
	payload := []byte("tampered payload")
	fake := newFakeReleaseServer(t, payload, func(base, digest string) string {
		return linuxReleaseJSON(base, digest, "sha256:0000000000000000000000000000000000000000000000000000000000000000")
	})
	service := newTestService(t, fake.URL)
	_, err := service.Download(context.Background(), "0.6.34", "attempt-2", "", nil)
	if err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("err = %v, want checksum mismatch", err)
	}
	if _, err := os.Stat(filepath.Join(service.Root(), "attempt-2")); !os.IsNotExist(err) {
		t.Fatalf("cache dir should be removed after mismatch, stat err = %v", err)
	}
}

func TestDownloadRefusesWithoutChecksum(t *testing.T) {
	payload := []byte("unverified payload")
	fake := newFakeReleaseServer(t, payload, func(base, digest string) string {
		return linuxReleaseJSON(base, digest, "")
	})
	service := newTestService(t, fake.URL)
	_, err := service.Download(context.Background(), "0.6.34", "attempt-3", "", nil)
	if err == nil || !strings.Contains(err.Error(), "no checksum") {
		t.Fatalf("err = %v, want refusal without checksum", err)
	}
	if fake.assetHits.Load() != 0 {
		t.Fatal("asset must not be downloaded when no checksum is available")
	}
}

func TestDownloadSidecarChecksumContent(t *testing.T) {
	payload := []byte("sidecar content payload")
	sum := sha256.Sum256(payload)
	digest := hex.EncodeToString(sum[:])
	mux := http.NewServeMux()
	var server *httptest.Server
	mux.HandleFunc("/repos/sugaryiiy/magpiedb/releases/latest", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	mux.HandleFunc("/repos/sugaryiiy/magpiedb/releases", func(w http.ResponseWriter, r *http.Request) {
		release := map[string]any{
			"tag_name": "v0.6.34", "name": "MagpieDB 0.6.34", "html_url": "https://example.com/release", "body": "notes",
			"prerelease": false, "draft": false, "published_at": "2026-10-24T03:00:00Z",
			"assets": []any{
				map[string]any{"name": "MagpieDB-0.6.34-Linux-x64.tar.gz", "browser_download_url": server.URL + "/asset", "size": len(payload)},
				map[string]any{"name": "MagpieDB-0.6.34-Linux-x64.tar.gz.sha256", "browser_download_url": server.URL + "/sidecar", "size": 80},
			},
		}
		data, _ := json.Marshal([]any{release})
		_, _ = w.Write(data)
	})
	mux.HandleFunc("/asset", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(payload) })
	mux.HandleFunc("/sidecar", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, "%s  MagpieDB-0.6.34-Linux-x64.tar.gz\n", digest)
	})
	server = httptest.NewServer(mux)
	defer server.Close()
	service := newTestService(t, server.URL)
	downloaded, err := service.Download(context.Background(), "0.6.34", "attempt-5", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if downloaded.Version != "0.6.34" {
		t.Fatalf("downloaded = %+v", downloaded)
	}
}

func TestDownloadCancellation(t *testing.T) {
	payload := []byte("slow payload")
	release := func(base, digest string) string { return linuxReleaseJSON(base, digest, "sha256:"+digest) }
	sum := sha256.Sum256(payload)
	mux := http.NewServeMux()
	var server *httptest.Server
	mux.HandleFunc("/repos/sugaryiiy/magpiedb/releases/latest", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	mux.HandleFunc("/repos/sugaryiiy/magpiedb/releases", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(release(server.URL, hex.EncodeToString(sum[:]))))
	})
	mux.HandleFunc("/asset", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(payload[:4])
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	})
	server = httptest.NewServer(mux)
	defer server.Close()
	service := newTestService(t, server.URL)
	done := make(chan error, 1)
	go func() {
		_, err := service.Download(context.Background(), "0.6.34", "attempt-6", "", nil)
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for fake := service; ; {
		_ = fake
		if _, err := os.Stat(filepath.Join(service.Root(), "attempt-6")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("download did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	service.Cancel()
	select {
	case err := <-done:
		if err == nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context cancellation", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("download did not observe cancellation")
	}
	if _, err := os.Stat(filepath.Join(service.Root(), "attempt-6")); !os.IsNotExist(err) {
		t.Fatalf("cancelled cache should be removed, stat err = %v", err)
	}
}
