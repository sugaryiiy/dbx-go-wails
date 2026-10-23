package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func writeTestManifest(t *testing.T, service *Service, entry *manifest) {
	t.Helper()
	if err := os.MkdirAll(service.cacheDir(entry.CacheID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := service.writeManifest(entry); err != nil {
		t.Fatal(err)
	}
}

func makeTarGz(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gz)
	if err := tarWriter.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestPrepareInstall(t *testing.T) {
	if runtime := serviceRuntime(); runtime != "linux/amd64" {
		t.Skipf("fixture targets linux/amd64, running %s", runtime)
	}
	root := filepath.Join(t.TempDir(), "updates")
	service := NewService(root, NewGitHubClient("", "0.6.33"))
	newBinary := []byte("#!/bin/sh\necho new\n")
	packageBytes := makeTarGz(t, "magpiedb", newBinary)
	sum := sha256.Sum256(packageBytes)
	assetName := "MagpieDB-0.6.34-Linux-x64.tar.gz"
	if err := os.MkdirAll(service.cacheDir("attempt-1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(service.cacheDir("attempt-1"), assetName), packageBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestManifest(t, service, &manifest{CacheID: "attempt-1", Version: "0.6.34", AssetName: assetName, SHA256: hex.EncodeToString(sum[:])})

	if _, err := service.PrepareInstall("attempt-1", "0.6.35"); err == nil || !strings.Contains(err.Error(), "version changed") {
		t.Fatalf("version mismatch err = %v", err)
	}
	jobPath, err := service.PrepareInstall("attempt-1", "0.6.34")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(jobPath)
	if err != nil {
		t.Fatal(err)
	}
	var job InstallJob
	if err := json.Unmarshal(data, &job); err != nil {
		t.Fatal(err)
	}
	if job.Version != "0.6.34" || job.WaitPID != os.Getpid() {
		t.Fatalf("job = %+v", job)
	}
	staged, err := os.ReadFile(job.Staged)
	if err != nil || string(staged) != string(newBinary) {
		t.Fatalf("staged = %q, err %v", staged, err)
	}
	pending, err := service.readPending()
	if err != nil || pending == nil || pending.State != pendingStaged || pending.Version != "0.6.34" {
		t.Fatalf("pending = %+v, err %v", pending, err)
	}
}

func TestPrepareInstallRejectsCorruptCache(t *testing.T) {
	if runtime := serviceRuntime(); runtime != "linux/amd64" {
		t.Skipf("fixture targets linux/amd64, running %s", runtime)
	}
	root := filepath.Join(t.TempDir(), "updates")
	service := NewService(root, NewGitHubClient("", "0.6.33"))
	assetName := "MagpieDB-0.6.34-Linux-x64.tar.gz"
	if err := os.MkdirAll(service.cacheDir("attempt-2"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(service.cacheDir("attempt-2"), assetName), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeTestManifest(t, service, &manifest{CacheID: "attempt-2", Version: "0.6.34", AssetName: assetName, SHA256: strings.Repeat("0", 64)})
	if _, err := service.PrepareInstall("attempt-2", "0.6.34"); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("err = %v, want checksum mismatch", err)
	}
}

func serviceRuntime() string {
	service := NewService("", nil)
	return service.goos + "/" + service.goarch
}

func exitImmediately(t *testing.T) int {
	t.Helper()
	command := exec.Command("sleep", "0.2")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = command.Wait() }()
	return command.Process.Pid
}

func writeJob(t *testing.T, job *InstallJob) string {
	t.Helper()
	data, err := json.Marshal(job)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(job.Root, "job.json")
	if err := os.MkdirAll(job.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHelperSwapAndConfirm(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "updates")
	target := filepath.Join(dir, "magpiedb")
	oldBinary := []byte("#!/bin/sh\necho old\n")
	newBinary := []byte("#!/bin/sh\necho new\n")
	if err := os.WriteFile(target, oldBinary, 0o755); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(dir, "staging", "magpiedb")
	if err := os.MkdirAll(filepath.Dir(staged), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(staged, newBinary, 0o755); err != nil {
		t.Fatal(err)
	}
	job := &InstallJob{Version: "0.6.34", Root: root, Target: target, Staged: staged, Backup: target + ".magpiedb-backup", WaitPID: exitImmediately(t)}
	if err := RunHelper(writeJob(t, job)); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != string(newBinary) {
		t.Fatalf("target after swap = %q, err %v", content, err)
	}
	backup, err := os.ReadFile(job.Backup)
	if err != nil || string(backup) != string(oldBinary) {
		t.Fatalf("backup = %q, err %v", backup, err)
	}
	service := NewService(root, nil)
	pending, err := service.readPending()
	if err != nil || pending == nil || pending.State != pendingSwapped {
		t.Fatalf("pending = %+v, err %v", pending, err)
	}
	// The new build confirms its own update: backup and record are dropped.
	if err := service.ConfirmPending("0.6.34"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(job.Backup); !os.IsNotExist(err) {
		t.Fatalf("backup should be removed after confirmation, err = %v", err)
	}
	if pending, _ := service.readPending(); pending != nil {
		t.Fatalf("pending should be removed after confirmation: %+v", pending)
	}
}

func TestHelperRollbackOnMissingPayload(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "updates")
	target := filepath.Join(dir, "magpiedb")
	oldBinary := []byte("#!/bin/sh\necho old\n")
	if err := os.WriteFile(target, oldBinary, 0o755); err != nil {
		t.Fatal(err)
	}
	job := &InstallJob{Version: "0.6.34", Root: root, Target: target, Staged: filepath.Join(dir, "staging", "missing"), Backup: target + ".magpiedb-backup", WaitPID: exitImmediately(t)}
	if err := RunHelper(writeJob(t, job)); err == nil {
		t.Fatal("helper should fail when the staged payload is missing")
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != string(oldBinary) {
		t.Fatalf("target after failed swap = %q, err %v", content, err)
	}
	service := NewService(root, nil)
	if got := service.PendingFailure(); got != "0.6.34" {
		t.Fatalf("PendingFailure = %q, want 0.6.34", got)
	}
	if err := service.ClearPendingFailure(); err != nil {
		t.Fatal(err)
	}
	if got := service.PendingFailure(); got != "" {
		t.Fatalf("PendingFailure after clear = %q", got)
	}
}

func TestConfirmPendingMarksMismatchedSwapFailed(t *testing.T) {
	root := filepath.Join(t.TempDir(), "updates")
	service := NewService(root, nil)
	backup := filepath.Join(t.TempDir(), "MagpieDB.app.magpiedb-backup")
	if err := os.MkdirAll(backup, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := service.writePending(&pendingInstall{Version: "0.6.34", Backup: backup, State: pendingSwapped}); err != nil {
		t.Fatal(err)
	}
	if err := service.ConfirmPending("0.6.33"); err != nil {
		t.Fatal(err)
	}
	if got := service.PendingFailure(); got != "0.6.34" {
		t.Fatalf("PendingFailure = %q, want 0.6.34", got)
	}
	// A merely staged record (quit cancelled) is dropped silently.
	if err := service.writePending(&pendingInstall{Version: "0.6.34", State: pendingStaged}); err != nil {
		t.Fatal(err)
	}
	if err := service.ConfirmPending("0.6.33"); err != nil {
		t.Fatal(err)
	}
	if pending, _ := service.readPending(); pending != nil {
		t.Fatalf("staged pending should be dropped, got %+v", pending)
	}
}
