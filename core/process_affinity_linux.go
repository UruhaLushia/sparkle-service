//go:build linux

package core

import (
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

func currentProcessCPUAffinity() ([]int, error) {
	var mask unix.CPUSet
	if err := unix.SchedGetaffinity(0, &mask); err != nil {
		return nil, err
	}
	cpus := make([]int, 0, mask.Count())
	for cpu := range 1024 {
		if mask.IsSet(cpu) {
			cpus = append(cpus, cpu)
		}
	}
	return cpus, nil
}

func setProcessCPUAffinity(pid int32, cpus []int, restore ...[]int) error {
	if pid <= 0 {
		return fmt.Errorf("无效的核心进程 PID：%d", pid)
	}

	var requested unix.CPUSet
	if len(cpus) == 0 {
		restoreCPUs := restore
		if len(restoreCPUs) == 0 || len(restoreCPUs[0]) == 0 {
			if err := unix.SchedGetaffinity(0, &requested); err != nil {
				return fmt.Errorf("读取 service CPU 集合失败：%w", err)
			}
		} else {
			for _, cpu := range restoreCPUs[0] {
				requested.Set(cpu)
			}
		}
	} else {
		for _, cpu := range cpus {
			if cpu < 0 || cpu >= 1024 {
				return fmt.Errorf("CPU 编号超出支持范围：%d", cpu)
			}
			requested.Set(cpu)
		}
	}
	if used, err := setCPUCgroup(pid, cpus); used {
		if err != nil {
			return fmt.Errorf("更新核心 cgroup CPU 集合失败：%w", err)
		}
		if len(cpus) > 0 {
			return nil
		}
	}

	// A process can create a thread while its existing threads are being
	// updated. Re-scan a few times so a thread that inherited the old mask is
	// covered before returning.
	for range 3 {
		entries, err := os.ReadDir(fmt.Sprintf("/proc/%d/task", pid))
		if err != nil {
			return fmt.Errorf("读取核心进程线程列表失败：%w", err)
		}
		for _, entry := range entries {
			tid, err := strconv.Atoi(entry.Name())
			if err != nil {
				continue
			}
			if err := unix.SchedSetaffinity(tid, &requested); err != nil {
				if err == unix.ESRCH {
					continue
				}
				return fmt.Errorf("绑定线程 %d 到 CPU %v 失败：%w", tid, cpus, err)
			}
			var actual unix.CPUSet
			if err := unix.SchedGetaffinity(tid, &actual); err != nil {
				if err == unix.ESRCH {
					continue
				}
				return fmt.Errorf("读取线程 %d CPU 绑定结果失败：%w", tid, err)
			}
			if actual != requested {
				return fmt.Errorf("线程 %d 无法绑定全部指定 CPU %v，请检查 CPU 是否在线及 cpuset 限制", tid, cpus)
			}
		}
	}
	return nil
}
