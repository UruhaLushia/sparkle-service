//go:build darwin

package process

import (
	"fmt"
	"runtime"
)

func CurrentCPUAffinity() ([]int, error) {
	return allCPUs(), nil
}

func SetCPUAffinity(pid int32, cpus []int, restore ...[]int) error {
	if len(cpus) == 0 {
		return nil
	}
	return fmt.Errorf("macOS 不支持按逻辑 CPU 硬绑定（请求 %v）；Mach affinity tag 只能提供软调度分组", cpus)
}

func allCPUs() []int {
	cpus := make([]int, runtime.NumCPU())
	for cpu := range cpus {
		cpus[cpu] = cpu
	}
	return cpus
}
