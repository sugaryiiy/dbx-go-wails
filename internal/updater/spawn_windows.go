//go:build windows

package updater

import (
	"os"
	"os/exec"
	"syscall"
)

// SpawnHelper starts the detached update helper in its own process group
// so it survives the main process it is about to replace.
func SpawnHelper(jobPath string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	command := exec.Command(executable, HelperArg, jobPath)
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x00000008 | 0x00000200} // DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP
	return command.Start()
}
