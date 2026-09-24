//go:build windows

package sys

import (
	"encoding/binary"
	"unsafe"

	"golang.org/x/sys/windows"
)

var getSystemCPUSetInformation = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetSystemCpuSetInformation")

func cpuCoreClasses() map[int]int {
	var size uint32
	_, _, _ = getSystemCPUSetInformation.Call(0, 0, uintptr(unsafe.Pointer(&size)), 0, 0)
	if size == 0 {
		return nil
	}

	data := make([]byte, size)
	ret, _, _ := getSystemCPUSetInformation.Call(
		uintptr(unsafe.Pointer(&data[0])),
		uintptr(size),
		uintptr(unsafe.Pointer(&size)),
		0,
		0,
	)
	if ret == 0 {
		return nil
	}

	classes := make(map[int]int)
	for offset := 0; offset+20 <= len(data); {
		recordSize := int(binary.LittleEndian.Uint32(data[offset : offset+4]))
		if recordSize < 20 || offset+recordSize > len(data) {
			break
		}
		// CPU_SET_INFORMATION_TYPE 0 is CpuSetInformation.
		if binary.LittleEndian.Uint32(data[offset+4:offset+8]) == 0 {
			group := binary.LittleEndian.Uint16(data[offset+12 : offset+14])
			logical := data[offset+14]
			efficiencyClass := data[offset+18]
			id := int(group)*64 + int(logical)
			classes[id] = int(efficiencyClass)
		}
		offset += recordSize
	}
	return classes
}
