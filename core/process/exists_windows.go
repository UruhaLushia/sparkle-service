//go:build windows

package process

import (
	"fmt"

	"golang.org/x/sys/windows"
)

func Exists(pid int32) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err == windows.ERROR_INVALID_PARAMETER {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("检查进程 %d 是否存在失败：%w", pid, err)
	}
	defer windows.CloseHandle(handle)
	// An exited process can still be opened while another handle keeps it alive.
	state, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		return false, fmt.Errorf("检查进程 %d 是否退出失败：%w", pid, err)
	}
	return state != windows.WAIT_OBJECT_0, nil
}
