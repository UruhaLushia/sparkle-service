//go:build linux

package sys

import (
	"os"

	"golang.org/x/sys/unix"
)

func currentProcessCPUAffinity() ([]int32, bool, error) {
	var mask unix.CPUSet
	if err := unix.SchedGetaffinity(os.Getpid(), &mask); err != nil {
		return nil, true, err
	}
	ids := make([]int32, 0, mask.Count())
	for id := range 1024 {
		if mask.IsSet(id) {
			ids = append(ids, int32(id))
		}
	}
	return ids, true, nil
}
