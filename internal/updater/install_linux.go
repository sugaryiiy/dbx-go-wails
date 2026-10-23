//go:build linux

package updater

import (
	"os"
	"os/exec"
)

// extractStagedPackage unpacks the Linux tarball and locates the binary.
func extractStagedPackage(assetPath, stagingDir, goos string) (string, error) {
	if err := extractTarGzPackage(assetPath, stagingDir); err != nil {
		return "", err
	}
	return findExtractedFile(stagingDir, "magpiedb")
}

func stageNextToTarget(job *InstallJob) (string, error) {
	near := job.Target + ".magpiedb-new"
	if err := copyFileNextToTarget(job.Staged, near); err != nil {
		return "", err
	}
	return near, nil
}

func platformPostSwap(job *InstallJob, logf func(string, ...any)) {
	if err := os.Chmod(job.Target, 0o755); err != nil {
		logf("chmod on new binary failed: %v", err)
	}
}

func launchInstalled(job *InstallJob) error {
	command := exec.Command(job.Target)
	command.Stdout = nil
	command.Stderr = nil
	return command.Start()
}
