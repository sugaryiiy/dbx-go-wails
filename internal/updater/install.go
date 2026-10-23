package updater

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// InstallJob is the hand-off file between the running app and the detached
// helper process (magpiedb --apply-update <job.json>). The helper waits for
// the main process to exit, swaps Target with Staged, and relaunches.
type InstallJob struct {
	Version string `json:"version"`
	// Root is the update cache root; pending.json and install.log live there.
	Root string `json:"root"`
	// Target is the installed payload that gets replaced: the .app bundle
	// directory on macOS, the executable file on Windows/Linux.
	Target string `json:"target"`
	// Staged is the extracted replacement payload (same kind as Target).
	Staged string `json:"staged"`
	// Backup is where Target is parked during the swap. It sits next to
	// Target so both renames stay on one volume.
	Backup string `json:"backup"`
	// WaitPID is the main process the helper waits on before swapping.
	WaitPID int `json:"wait_pid"`
	// SkipLaunch is a test-only switch: when true the helper swaps the
	// payload but does not relaunch it. Tests use fake payload files that
	// are not executable binaries, so relaunching them would fail on
	// Windows (run 39925413088). Production always leaves this false.
	SkipLaunch bool `json:"skip_launch,omitempty"`
}

// pendingInstall is the durable record of an in-flight or finished update
// (<root>/pending.json). The next process start confirms it: a "swapped"
// record whose version matches the running build closes the loop (backup
// deleted); anything else surfaces as a failure the user can dismiss.
type pendingInstall struct {
	Version string `json:"version"`
	Target  string `json:"target"`
	Backup  string `json:"backup"`
	State   string `json:"state"` // staged | swapped | failed
	Updated int64  `json:"updated_at"`
}

const (
	pendingStaged  = "staged"
	pendingSwapped = "swapped"
	pendingFailed  = "failed"
)

func (s *Service) pendingPath() string { return filepath.Join(s.root, "pending.json") }

func (s *Service) readPending() (*pendingInstall, error) {
	data, err := os.ReadFile(s.pendingPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var pending pendingInstall
	if err := json.Unmarshal(data, &pending); err != nil {
		return nil, nil
	}
	return &pending, nil
}

func (s *Service) writePending(pending *pendingInstall) error {
	pending.Updated = time.Now().UnixMilli()
	data, err := json.MarshalIndent(pending, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return err
	}
	return os.WriteFile(s.pendingPath(), data, 0o600)
}

// PrepareInstall verifies a downloaded package (version and checksum),
// extracts it into the cache's staging area, writes pending.json and the
// helper job, and returns the job path. The caller spawns the helper and
// then drives the normal quit flow; the helper performs the swap once this
// process has exited.
func (s *Service) PrepareInstall(cacheID, expectedVersion string) (string, error) {
	entry, err := s.loadManifest(cacheID)
	if err != nil {
		return "", err
	}
	if entry.Version != expectedVersion {
		return "", fmt.Errorf("downloaded update version changed (expected %s, cache holds %s); check for updates again", expectedVersion, entry.Version)
	}
	assetPath := filepath.Join(s.cacheDir(cacheID), entry.AssetName)
	if err := verifyFileChecksum(assetPath, entry.SHA256); err != nil {
		return "", err
	}
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	target, err := platformTarget(executable, s.goos)
	if err != nil {
		return "", err
	}
	backup := target + ".magpiedb-backup"
	if _, err := os.Stat(backup); err == nil {
		return "", fmt.Errorf("a backup from a previous update still exists at %s; finish or dismiss that update before installing another", backup)
	}
	stagingDir := filepath.Join(s.cacheDir(cacheID), "staging")
	_ = os.RemoveAll(stagingDir)
	if err := os.MkdirAll(stagingDir, 0o700); err != nil {
		return "", err
	}
	staged, err := extractStagedPackage(assetPath, stagingDir, s.goos)
	if err != nil {
		return "", err
	}
	job := &InstallJob{
		Version: entry.Version,
		Root:    s.root,
		Target:  target,
		Staged:  staged,
		Backup:  backup,
		WaitPID: os.Getpid(),
	}
	jobPath := filepath.Join(s.cacheDir(cacheID), "job.json")
	jobData, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(jobPath, jobData, 0o600); err != nil {
		return "", err
	}
	if err := s.writePending(&pendingInstall{Version: entry.Version, Target: target, Backup: backup, State: pendingStaged}); err != nil {
		return "", err
	}
	return jobPath, nil
}

func verifyFileChecksum(path, expectedSHA256 string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, io.LimitReader(file, maxAssetBytes+1)); err != nil {
		return err
	}
	sum := hex.EncodeToString(hash.Sum(nil))
	if sum != expectedSHA256 {
		return fmt.Errorf("cached update package SHA-256 mismatch (got %s); download it again", sum)
	}
	return nil
}

// ConfirmPending runs at startup and resolves the pending record left by a
// previous install: a swap that landed on this very version is confirmed
// (backup and staging leftovers removed); a swap whose version does not
// match the running build is marked failed so the UI can report the
// rollback; a merely staged install (the quit was cancelled) is dropped.
func (s *Service) ConfirmPending(ownVersion string) error {
	pending, err := s.readPending()
	if err != nil || pending == nil {
		return err
	}
	switch pending.State {
	case pendingSwapped:
		if pending.Version == ownVersion {
			_ = os.RemoveAll(pending.Backup)
			_ = os.Remove(s.pendingPath())
			return nil
		}
		pending.State = pendingFailed
		return s.writePending(pending)
	case pendingStaged:
		_ = os.Remove(s.pendingPath())
		return nil
	}
	return nil
}

// PendingFailure returns the version of a failed update awaiting the
// user's attention, or "".
func (s *Service) PendingFailure() string {
	pending, err := s.readPending()
	if err != nil || pending == nil || pending.State != pendingFailed {
		return ""
	}
	return pending.Version
}

// ClearPendingFailure dismisses a failed update: the pending record and
// any backup leftovers are removed. The backup is a copy of the version
// that is already running again, so dropping it loses nothing.
func (s *Service) ClearPendingFailure() error {
	pending, err := s.readPending()
	if err != nil {
		return err
	}
	if pending != nil && pending.State == pendingFailed {
		_ = os.RemoveAll(pending.Backup)
	}
	return os.Remove(s.pendingPath())
}

// platformTarget resolves what an install replaces for the running build:
// the enclosing .app bundle on macOS, the executable file itself elsewhere.
// A bare macOS binary (not inside a bundle) has no installable layout and
// is rejected, matching the manual-only classification of such installs.
func platformTarget(executable, goos string) (string, error) {
	if goos != "darwin" {
		return executable, nil
	}
	dir := filepath.Dir(executable)
	for {
		if strings.HasSuffix(dir, ".app") {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("the running build is not inside an application bundle; reinstall from the release page instead")
		}
		dir = parent
	}
}
