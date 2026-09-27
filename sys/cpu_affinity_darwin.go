//go:build darwin

package sys

import "runtime"

func currentProcessCPUAffinity() ([]int32, bool, error) {
	ids := make([]int32, runtime.NumCPU())
	for id := range ids {
		ids[id] = int32(id)
	}
	return ids, false, nil
}
