//go:build !windows && !linux && !darwin

package process

func CurrentCPUAffinity() ([]int, error) {
	return nil, nil
}

func SetCPUAffinity(pid int32, cpus []int, restore ...[]int) error {
	return nil
}
