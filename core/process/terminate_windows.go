//go:build windows

package process

import (
	"fmt"

	"golang.org/x/sys/windows"
)

func Terminate(pid int32) error {
	if pid <= 0 {
		return nil
	}
	handle, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		if err == windows.ERROR_INVALID_PARAMETER {
			return nil
		}
		return fmt.Errorf("打开核心进程失败：%w", err)
	}
	defer windows.CloseHandle(handle)
	if err := windows.TerminateProcess(handle, 1); err != nil && err != windows.ERROR_INVALID_HANDLE {
		return fmt.Errorf("终止核心进程失败：%w", err)
	}
	return nil
}
