//go:build windows

package updater

import (
	"fmt"
	"syscall"
	"time"
)

var (
	kernel32             = syscall.NewLazyDLL("kernel32.dll")
	procOpenProcess      = kernel32.NewProc("OpenProcess")
	procWaitForSingleObj = kernel32.NewProc("WaitForSingleObject")
	procCloseHandle      = kernel32.NewProc("CloseHandle")
)

const (
	synchronizeAccess   = 0x00100000
	processQueryLimited = 0x1000
	waitObject0         = 0x00000000
	waitTimeoutResult   = 0x00000102
	errorInvalidParam   = 87
	waitSlice           = 50 * time.Millisecond
)

// waitForProcessExit waits on a SYNCHRONIZE handle for the main process,
// in short slices so the overall timeout matches the Unix implementation.
func waitForProcessExit(pid int, timeout time.Duration) error {
	if pid <= 0 {
		return nil
	}
	handle, _, callErr := procOpenProcess.Call(synchronizeAccess|processQueryLimited, 0, uintptr(uint32(pid)))
	if handle == 0 {
		// ERROR_INVALID_PARAMETER: no such process — it already exited.
		if errno, ok := callErr.(syscall.Errno); ok && errno == errorInvalidParam {
			return nil
		}
		return fmt.Errorf("open main process %d: %w", pid, callErr)
	}
	defer procCloseHandle.Call(handle)
	deadline := time.Now().Add(timeout)
	for {
		result, _, _ := procWaitForSingleObj.Call(handle, uintptr(waitSlice.Milliseconds()))
		if result == waitObject0 {
			return nil
		}
		if result != waitTimeoutResult {
			return fmt.Errorf("wait for main process %d returned %d", pid, result)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("main process %d did not exit within %s", pid, timeout)
		}
	}
}
