//go:build darwin

package updater

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// extractStagedPackage mounts the DMG read-only and copies the .app out
// with ditto, which preserves permissions and extended attributes. The
// staged payload is the bundle directory.
func extractStagedPackage(assetPath, stagingDir, goos string) (string, error) {
	mount := filepath.Join(stagingDir, ".mount")
	if err := os.MkdirAll(mount, 0o700); err != nil {
		return "", err
	}
	attach := exec.Command("hdiutil", "attach", "-nobrowse", "-readonly", "-mountpoint", mount, assetPath)
	if output, err := attach.CombinedOutput(); err != nil {
		return "", fmt.Errorf("mount update DMG: %v (%s)", err, strings.TrimSpace(string(output)))
	}
	defer func() {
		_ = exec.Command("hdiutil", "detach", mount, "-quiet").Run()
	}()
	entries, err := os.ReadDir(mount)
	if err != nil {
		return "", err
	}
	var bundle string
	for _, entry := range entries {
		if entry.IsDir() && strings.HasSuffix(entry.Name(), ".app") {
			bundle = filepath.Join(mount, entry.Name())
			break
		}
	}
	if bundle == "" {
		return "", fmt.Errorf("update DMG does not contain an application bundle")
	}
	staged := filepath.Join(stagingDir, filepath.Base(bundle))
	if output, err := exec.Command("ditto", bundle, staged).CombinedOutput(); err != nil {
		return "", fmt.Errorf("copy application bundle out of the DMG: %v (%s)", err, strings.TrimSpace(string(output)))
	}
	return staged, nil
}

// stageNextToTarget copies the staged bundle next to the installed one so
// the swap renames stay on one volume (the cache usually lives on the
// user's data volume, the app in /Applications).
func stageNextToTarget(job *InstallJob) (string, error) {
	near := job.Target + ".magpiedb-new"
	if output, err := exec.Command("ditto", job.Staged, near).CombinedOutput(); err != nil {
		return "", fmt.Errorf("stage new bundle next to the current one: %v (%s)", err, strings.TrimSpace(string(output)))
	}
	return near, nil
}

func platformPostSwap(job *InstallJob, logf func(string, ...any)) {
	// App-internal downloads carry no quarantine attribute, but tooling in
	// the packaging chain can add one; strip it best-effort so Gatekeeper
	// does not re-evaluate the replaced bundle.
	if err := exec.Command("xattr", "-dr", "com.apple.quarantine", job.Target).Run(); err != nil {
		logf("xattr quarantine cleanup failed (ignored): %v", err)
	}
}

func launchInstalled(job *InstallJob) error {
	return exec.Command("open", job.Target).Start()
}
