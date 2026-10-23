//go:build darwin || linux

package updater

import (
	"os"
	"os/exec"
	"syscall"
)

// SpawnHelper starts the detached update helper as a new session so it
// survives the main process it is about to replace.
func SpawnHelper(jobPath string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	command := exec.Command(executable, HelperArg, jobPath)
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return command.Start()
}
