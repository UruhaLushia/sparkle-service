//go:build windows

package core

import (
	"fmt"
	"math/bits"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

var setProcessAffinityMask = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetProcessAffinityMask")
var getProcessAffinityMask = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessAffinityMask")

func currentProcessCPUAffinity() ([]int, error) {
	return nil, nil
}

func setProcessCPUAffinity(pid int32, cpus []int, restore ...[]int) error {
	var mask uintptr
	if len(cpus) == 0 {
		serviceHandle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(os.Getpid()))
		if err != nil {
			return fmt.Errorf("打开 service 进程失败：%w", err)
		}
		defer windows.CloseHandle(serviceHandle)
		var systemMask uintptr
		result, _, callErr := getProcessAffinityMask.Call(
			uintptr(serviceHandle),
			uintptr(unsafe.Pointer(&mask)),
			uintptr(unsafe.Pointer(&systemMask)),
		)
		if result == 0 {
			return fmt.Errorf("GetProcessAffinityMask 失败：%w", callErr)
		}
	} else {
		for _, cpu := range cpus {
			if cpu < 0 || cpu >= bits.UintSize {
				return fmt.Errorf("CPU 编号 %d 超出 Windows 进程亲和性掩码范围 0-%d", cpu, bits.UintSize-1)
			}
			mask |= uintptr(1) << uint(cpu)
		}
	}

	handle, err := windows.OpenProcess(windows.PROCESS_SET_INFORMATION, false, uint32(pid))
	if err != nil {
		return fmt.Errorf("打开核心进程失败：%w", err)
	}
	defer windows.CloseHandle(handle)

	result, _, callErr := setProcessAffinityMask.Call(uintptr(handle), mask)
	if result == 0 {
		if callErr == nil {
			callErr = windows.GetLastError()
		}
		return fmt.Errorf("SetProcessAffinityMask 失败：%w", callErr)
	}

	return nil
}
