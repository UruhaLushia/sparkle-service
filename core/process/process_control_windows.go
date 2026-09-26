//go:build windows

package process

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

type windowsProcessController struct {
	job windows.Handle
}

func NewController() Controller {
	return &windowsProcessController{}
}

func ConfigureCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_BREAKAWAY_FROM_JOB | windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
	}
}

func SetPriority(pid int32, priority string) error {
	if priority == "" || priority == "PRIORITY_NORMAL" {
		return nil
	}

	priorityClass, ok := map[string]uint32{
		"PRIORITY_LOW":          windows.IDLE_PRIORITY_CLASS,
		"PRIORITY_BELOW":        windows.BELOW_NORMAL_PRIORITY_CLASS,
		"PRIORITY_NORMAL":       windows.NORMAL_PRIORITY_CLASS,
		"PRIORITY_ABOVE":        windows.ABOVE_NORMAL_PRIORITY_CLASS,
		"PRIORITY_HIGH":         windows.HIGH_PRIORITY_CLASS,
		"PRIORITY_HIGHEST":      windows.REALTIME_PRIORITY_CLASS,
		"PRIORITY_IDLE":         windows.IDLE_PRIORITY_CLASS,
		"PRIORITY_BELOW_NORMAL": windows.BELOW_NORMAL_PRIORITY_CLASS,
		"PRIORITY_ABOVE_NORMAL": windows.ABOVE_NORMAL_PRIORITY_CLASS,
	}[priority]
	if !ok {
		return fmt.Errorf("不支持的进程优先级: %s", priority)
	}

	handle, err := windows.OpenProcess(windows.PROCESS_SET_INFORMATION, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)

	return windows.SetPriorityClass(handle, priorityClass)
}

func (c *windowsProcessController) Attach(pid int32) error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("创建 Job Object 失败：%w", err)
	}

	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		windows.CloseHandle(job)
		return fmt.Errorf("配置 Job Object 失败：%w", err)
	}

	processHandle, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION,
		false,
		uint32(pid),
	)
	if err != nil {
		windows.CloseHandle(job)
		return fmt.Errorf("打开核心进程句柄失败：%w", err)
	}
	defer windows.CloseHandle(processHandle)

	if err := windows.AssignProcessToJobObject(job, processHandle); err != nil {
		windows.CloseHandle(job)
		return fmt.Errorf("绑定核心进程到 Job Object 失败：%w", err)
	}

	c.job = job
	return nil
}

func (c *windowsProcessController) PIDs() ([]int32, error) {
	if c.job == 0 {
		return nil, nil
	}

	for size := uintptr(1024); size <= 64*1024; size *= 2 {
		buffer := make([]byte, size)
		err := windows.QueryInformationJobObject(
			c.job,
			windows.JobObjectBasicProcessIdList,
			uintptr(unsafe.Pointer(&buffer[0])),
			uint32(len(buffer)),
			nil,
		)
		if err != nil {
			if errors.Is(err, windows.ERROR_MORE_DATA) || errors.Is(err, windows.ERROR_BAD_LENGTH) || errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) {
				continue
			}
			return nil, err
		}

		info := (*jobObjectBasicProcessIDList)(unsafe.Pointer(&buffer[0]))
		if info.NumberOfAssignedProcesses > info.NumberOfProcessIdsInList {
			continue
		}
		count := int(info.NumberOfProcessIdsInList)
		processIDs := unsafe.Slice(&info.ProcessIDList[0], count)
		pids := make([]int32, 0, count)
		for _, processID := range processIDs {
			if processID <= 0 {
				continue
			}
			pids = append(pids, int32(processID))
		}
		return pids, nil
	}

	return nil, fmt.Errorf("job object 进程列表过长")
}

func (c *windowsProcessController) Stop(pid int32) error {
	var closeErr error
	if c.job != 0 {
		closeErr = windows.CloseHandle(c.job)
		c.job = 0
	}

	if exited, err := waitForProcessExit(pid, 20, 100*time.Millisecond); err == nil && exited {
		return closeErr
	}

	cmd := exec.Command("taskkill", "/PID", fmt.Sprintf("%d", pid), "/T", "/F")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("终止核心进程失败：%w, output: %s", err, string(output))
	}

	return closeErr
}

type jobObjectBasicProcessIDList struct {
	NumberOfAssignedProcesses uint32
	NumberOfProcessIdsInList  uint32
	ProcessIDList             [1]uintptr
}

func (c *windowsProcessController) Close() error {
	if c.job == 0 {
		return nil
	}

	err := windows.CloseHandle(c.job)
	c.job = 0
	return err
}

func waitForProcessExit(pid int32, attempts int, interval time.Duration) (bool, error) {
	for range attempts {
		exists, err := Exists(pid)
		if err != nil {
			return false, err
		}
		if !exists {
			return true, nil
		}
		time.Sleep(interval)
	}

	return false, nil
}
