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
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err == windows.ERROR_INVALID_PARAMETER {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("检查进程 %d 是否存在失败：%w", pid, err)
	}
	if err := windows.CloseHandle(handle); err != nil {
		return true, fmt.Errorf("关闭进程 %d 查询句柄失败：%w", pid, err)
	}
	return true, nil
}
