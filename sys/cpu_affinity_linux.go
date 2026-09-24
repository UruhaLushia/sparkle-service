//go:build linux

package sys

import (
	"os"

	"github.com/shirou/gopsutil/v4/process"
)

func currentProcessCPUAffinity() ([]int32, bool, error) {
	proc, err := process.NewProcess(int32(os.Getpid()))
	if err != nil {
		return nil, false, err
	}
	ids, err := proc.CPUAffinity()
	return ids, true, err
}
