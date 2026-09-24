//go:build linux

package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

const cgroupRoot = "/sys/fs/cgroup"

type cpuCgroupState struct {
	path   string
	parent string
}

var cpuCgroups = struct {
	sync.Mutex
	items map[int32]cpuCgroupState
}{items: make(map[int32]cpuCgroupState)}

func setCPUCgroup(pid int32, cpus []int) (bool, error) {
	if os.Geteuid() != 0 {
		return false, nil
	}
	parent, err := processCgroupPath(pid)
	if err != nil {
		return false, err
	}
	if parent == "" || !strings.HasPrefix(parent, cgroupRoot+string(os.PathSeparator)) {
		return false, nil
	}
	if err := enableCPUSetController(); err != nil {
		return false, err
	}

	cpuCgroups.Lock()
	defer cpuCgroups.Unlock()
	state, exists := cpuCgroups.items[pid]
	if len(cpus) == 0 {
		if !exists {
			return false, nil
		}
		if err := writeCgroupPID(state.parent, pid); err != nil {
			return true, err
		}
		_ = os.Remove(state.path)
		delete(cpuCgroups.items, pid)
		return true, nil
	}

	if !exists {
		state = cpuCgroupState{
			path:   filepath.Join(cgroupRoot, "sparkle-service", "core-"+strconv.Itoa(int(pid))),
			parent: parent,
		}
		if err := os.MkdirAll(filepath.Dir(state.path), 0755); err != nil {
			return false, err
		}
		if err := os.Mkdir(state.path, 0755); err != nil && !os.IsExist(err) {
			return false, err
		}
		if err := copyCgroupValue(parent, state.path, "cpuset.mems.effective", "cpuset.mems"); err != nil {
			_ = os.Remove(state.path)
			return false, err
		}
		cpuCgroups.items[pid] = state
	}
	if err := writeCgroupValue(state.path, "cpuset.cpus", formatCPURange(cpus)); err != nil {
		return true, err
	}
	if err := writeCgroupPID(state.path, pid); err != nil {
		return true, err
	}
	return true, nil
}

func processCgroupPath(pid int32) (string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return "", err
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) == 3 && parts[0] == "0" {
			return filepath.Join(cgroupRoot, parts[2]), nil
		}
	}
	return "", nil
}

func enableCPUSetController() error {
	data, err := os.ReadFile(filepath.Join(cgroupRoot, "cgroup.subtree_control"))
	if err != nil {
		return err
	}
	if strings.Contains(string(data), "cpuset") {
		return nil
	}
	return writeCgroupValue(cgroupRoot, "cgroup.subtree_control", "+cpuset")
}

func copyCgroupValue(from, to, source, target string) error {
	data, err := os.ReadFile(filepath.Join(from, source))
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(to, target), data, 0644)
}

func writeCgroupValue(path, name, value string) error {
	return os.WriteFile(filepath.Join(path, name), []byte(value), 0644)
}

func writeCgroupPID(path string, pid int32) error {
	return writeCgroupValue(path, "cgroup.procs", strconv.FormatInt(int64(pid), 10))
}

func formatCPURange(cpus []int) string {
	if len(cpus) == 0 {
		return ""
	}
	var b strings.Builder
	start, previous := cpus[0], cpus[0]
	write := func() {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		if start == previous {
			fmt.Fprintf(&b, "%d", start)
		} else {
			fmt.Fprintf(&b, "%d-%d", start, previous)
		}
	}
	for _, cpu := range cpus[1:] {
		if cpu == previous+1 {
			previous = cpu
			continue
		}
		write()
		start, previous = cpu, cpu
	}
	write()
	return b.String()
}
