package updater

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// HelperArg is the hidden subcommand that turns the main binary into the
// update helper: magpiedb --apply-update <job.json>. main() dispatches on
// it before any GUI or server initialisation.
const HelperArg = "--apply-update"

// RunHelper executes one install job as the detached helper process
// (magpiedb --apply-update <job.json>). It is deliberately self-contained:
// it runs before any GUI or server initialisation, logs to
// <root>/install.log, swaps the package once the main process is gone, and
// relaunches — the new build on success, the restored old build after a
// rollback. A helper whose main process never exits (the user cancelled
// the quit) gives up after the wait timeout and leaves the pending record
// in "staged", which the next startup drops.
func RunHelper(jobPath string) error {
	data, err := os.ReadFile(jobPath)
	if err != nil {
		return err
	}
	var job InstallJob
	if err := json.Unmarshal(data, &job); err != nil {
		return fmt.Errorf("parse update job: %w", err)
	}
	log := helperLog(job.Root)
	defer func() {
		if log != nil {
			_ = log.Close()
		}
	}()
	logf := func(format string, args ...any) {
		fmt.Fprintf(log, "%s "+format+"\n", append([]any{time.Now().Format(time.RFC3339)}, args...)...)
	}
	logf("helper started for update to %s (target %s)", job.Version, job.Target)
	if err := waitForProcessExit(job.WaitPID, 10*time.Minute); err != nil {
		logf("aborted: %v", err)
		return err
	}
	service := &Service{root: job.Root}
	swapErr := swapPayload(&job, logf)
	if swapErr != nil {
		logf("swap failed: %v; rolling back", swapErr)
		if restoreErr := restoreBackup(&job); restoreErr != nil {
			logf("rollback failed: %v", restoreErr)
		}
		_ = service.writePending(&pendingInstall{Version: job.Version, Target: job.Target, Backup: job.Backup, State: pendingFailed})
		if launchErr := launchInstalled(&job); launchErr != nil {
			logf("relaunch after rollback failed: %v", launchErr)
		}
		return swapErr
	}
	_ = service.writePending(&pendingInstall{Version: job.Version, Target: job.Target, Backup: job.Backup, State: pendingSwapped})
	logf("swap complete; launching %s", job.Target)
	if err := launchInstalled(&job); err != nil {
		logf("launch failed: %v", err)
		return err
	}
	return nil
}

// swapPayload performs the package swap: the payload is first copied next
// to the target (same volume), then two renames exchange old and new. Any
// failure before the second rename leaves the installation untouched; a
// failure after it triggers the caller's rollback.
func swapPayload(job *InstallJob, logf func(string, ...any)) error {
	stagedNear, err := stageNextToTarget(job)
	if err != nil {
		return err
	}
	if err := os.Rename(job.Target, job.Backup); err != nil {
		_ = os.RemoveAll(stagedNear)
		return fmt.Errorf("move current version aside: %w", err)
	}
	if err := os.Rename(stagedNear, job.Target); err != nil {
		_ = os.Rename(job.Backup, job.Target)
		return fmt.Errorf("move new version into place: %w", err)
	}
	platformPostSwap(job, logf)
	return nil
}

// restoreBackup puts the pre-update payload back after a failed swap.
func restoreBackup(job *InstallJob) error {
	if _, err := os.Stat(job.Backup); err != nil {
		return err
	}
	_ = os.RemoveAll(job.Target)
	return os.Rename(job.Backup, job.Target)
}

func helperLog(root string) *os.File {
	if root == "" {
		return nil
	}
	_ = os.MkdirAll(root, 0o700)
	file, err := os.OpenFile(filepath.Join(root, "install.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil
	}
	return file
}
