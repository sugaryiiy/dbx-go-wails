//go:build darwin || linux

package updater

import (
	"errors"
	"fmt"
	"syscall"
	"time"
)

// waitForProcessExit polls until the process is gone. The helper is not
// the main process's parent, so it cannot Wait() on it; signal 0 probes
// existence. A zombie main process is reaped by its own parent promptly,
// so the probe is reliable enough with a generous timeout.
func waitForProcessExit(pid int, timeout time.Duration) error {
	if pid <= 0 {
		return nil
	}
	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil && !errors.Is(err, syscall.EPERM) {
			return fmt.Errorf("probe main process %d: %w", pid, err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("main process %d did not exit within %s", pid, timeout)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
