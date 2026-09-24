//go:build !linux && !windows

package sys

func cpuCoreClasses() map[int]int {
	return nil
}
