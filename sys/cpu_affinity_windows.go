//go:build windows

package sys

import (
	"fmt"
	"math/bits"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

var getProcessAffinityMask = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessAffinityMask")

func currentProcessCPUAffinity() ([]int32, bool, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(os.Getpid()))
	if err != nil {
		return nil, false, err
	}
	defer windows.CloseHandle(handle)

	var processMask uintptr
	var systemMask uintptr
	ret, _, callErr := getProcessAffinityMask.Call(
		uintptr(handle),
		uintptr(unsafe.Pointer(&processMask)),
		uintptr(unsafe.Pointer(&systemMask)),
	)
	if ret == 0 {
		return nil, false, fmt.Errorf("GetProcessAffinityMask 失败：%w", callErr)
	}

	ids := make([]int32, 0, bits.OnesCount(uint(processMask)))
	for cpu := range bits.UintSize {
		if processMask&(uintptr(1)<<cpu) != 0 {
			ids = append(ids, int32(cpu))
		}
	}
	return ids, true, nil
}
