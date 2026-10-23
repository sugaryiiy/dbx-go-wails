//go:build windows

package updater

import (
	"os/exec"
)

// extractStagedPackage unpacks the Windows zip and locates the executable.
// The running exe cannot be overwritten, but it can be renamed — the swap
// renames it aside, moves the staged exe into place, and the pending
// confirmation deletes the parked copy on the next start.
func extractStagedPackage(assetPath, stagingDir, goos string) (string, error) {
	if err := extractZipPackage(assetPath, stagingDir); err != nil {
		return "", err
	}
	return findExtractedFile(stagingDir, "magpiedb.exe")
}

func stageNextToTarget(job *InstallJob) (string, error) {
	near := job.Target + ".magpiedb-new"
	if err := copyFileNextToTarget(job.Staged, near); err != nil {
		return "", err
	}
	return near, nil
}

func platformPostSwap(job *InstallJob, logf func(string, ...any)) {}

func launchInstalled(job *InstallJob) error {
	return exec.Command(job.Target).Start()
}
