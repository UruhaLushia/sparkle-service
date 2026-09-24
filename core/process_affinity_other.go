//go:build !windows && !linux

package core

func currentProcessCPUAffinity() ([]int, error) {
	return nil, nil
}

func setProcessCPUAffinity(pid int32, cpus []int, restore ...[]int) error {
	return nil
}
