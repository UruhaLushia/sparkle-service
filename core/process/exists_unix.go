//go:build !windows

package process

import (
	"fmt"
	"syscall"
)

func Exists(pid int32) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	if err := syscall.Kill(int(pid), 0); err == nil {
		return true, nil
	} else if err == syscall.ESRCH {
		return false, nil
	} else if err == syscall.EPERM {
		return true, nil
	} else {
		return false, fmt.Errorf("检查进程 %d 是否存在失败：%w", pid, err)
	}
}
