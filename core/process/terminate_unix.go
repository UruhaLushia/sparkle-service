//go:build !windows

package process

import (
	"fmt"
	"syscall"
	"time"
)

func Terminate(pid int32) error {
	if pid <= 0 {
		return nil
	}

	if err := syscall.Kill(-int(pid), syscall.SIGTERM); err != nil && err != syscall.ESRCH {
		return err
	}
	if exited, err := waitForUnixProcessExit(pid, 20, 100*time.Millisecond); err != nil {
		return err
	} else if exited {
		return nil
	}

	if err := syscall.Kill(-int(pid), syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return err
	}
	if err := syscall.Kill(int(pid), syscall.SIGKILL); err != nil && err != syscall.ESRCH {
		return err
	}
	if exited, err := waitForUnixProcessExit(pid, 20, 100*time.Millisecond); err != nil {
		return err
	} else if !exited {
		return fmt.Errorf("等待进程退出超时")
	}
	return nil
}
