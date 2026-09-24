//go:build !windows && !linux

package core

func setProcessCPUAffinity(pid int32, cpus []int) error {
	return nil
}
