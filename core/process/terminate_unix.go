//go:build !windows

package process

import "syscall"

func Terminate(pid int32) error {
	if pid <= 0 {
		return nil
	}
	if err := syscall.Kill(-int(pid), syscall.SIGTERM); err != nil && err != syscall.ESRCH {
		return err
	}
	if err := syscall.Kill(-int(pid), syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return err
	}
	return nil
}
