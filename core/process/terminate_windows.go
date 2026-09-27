//go:build windows

package process

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

func Terminate(pid int32) error {
	if pid <= 0 {
		return nil
	}
	children, err := processTree(uint32(pid))
	if err != nil {
		return err
	}
	for i := len(children) - 1; i >= 0; i-- {
		if err := terminatePID(children[i]); err != nil {
			return err
		}
	}
	return terminatePID(uint32(pid))
}

func terminatePID(pid uint32) error {
	handle, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid)
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

func processTree(rootPID uint32) ([]uint32, error) {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("枚举核心进程树失败：%w", err)
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	if err := windows.Process32First(snapshot, &entry); err != nil {
		return nil, fmt.Errorf("读取进程快照失败：%w", err)
	}
	children := make(map[uint32][]uint32)
	for {
		children[entry.ParentProcessID] = append(children[entry.ParentProcessID], entry.ProcessID)
		if err := windows.Process32Next(snapshot, &entry); err != nil {
			if err != windows.ERROR_NO_MORE_FILES {
				return nil, fmt.Errorf("读取进程快照失败：%w", err)
			}
			break
		}
	}
	var tree []uint32
	queue := []uint32{rootPID}
	seen := map[uint32]bool{rootPID: true}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		for _, child := range children[pid] {
			if seen[child] {
				continue
			}
			seen[child] = true
			tree = append(tree, child)
			queue = append(queue, child)
		}
	}
	return tree, nil
}
