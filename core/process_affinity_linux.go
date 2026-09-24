//go:build linux

package core

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func setProcessCPUAffinity(pid int32, cpus []int) error {
	if pid <= 0 {
		return fmt.Errorf("无效的核心进程 PID：%d", pid)
	}

	var requested unix.CPUSet
	if len(cpus) == 0 {
		if err := unix.SchedGetaffinity(0, &requested); err != nil {
			return fmt.Errorf("读取 service CPU 集合失败：%w", err)
		}
	} else {
		for _, cpu := range cpus {
			if cpu < 0 || cpu >= 1024 {
				return fmt.Errorf("CPU 编号超出支持范围：%d", cpu)
			}
			requested.Set(cpu)
		}
	}

	if err := unix.SchedSetaffinity(int(pid), &requested); err != nil {
		return fmt.Errorf("绑定核心 CPU %v 失败：%w", cpus, err)
	}
	var actual unix.CPUSet
	if err := unix.SchedGetaffinity(int(pid), &actual); err != nil {
		return fmt.Errorf("读取核心 CPU 绑定结果失败：%w", err)
	}
	if actual != requested {
		return fmt.Errorf("无法绑定全部指定 CPU %v，请检查 CPU 是否在线及 cpuset 限制", cpus)
	}
	return nil
}
