//go:build !linux && !windows && !darwin

package sys

func currentProcessCPUAffinity() ([]int32, bool, error) {
	return nil, false, nil
}
