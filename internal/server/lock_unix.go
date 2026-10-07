//go:build !windows

package server

import (
	"os"
	"syscall"
)

func lockStore(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
func unlockStore(file *os.File) error { return syscall.Flock(int(file.Fd()), syscall.LOCK_UN) }
