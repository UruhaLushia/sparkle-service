//go:build !linux && !windows

package sys

func currentProcessCPUAffinity() ([]int32, bool, error) {
	return nil, false, nil
}
