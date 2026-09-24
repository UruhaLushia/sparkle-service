//go:build linux

package sys

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func cpuCoreClasses() map[int]int {
	classes := make(map[int]int)
	paths, _ := filepath.Glob("/sys/devices/system/cpu/cpu[0-9]*")
	for _, path := range paths {
		idText := strings.TrimPrefix(filepath.Base(path), "cpu")
		id, err := strconv.Atoi(idText)
		if err != nil {
			continue
		}
		for _, attribute := range []string{"topology/core_type", "cpu_capacity"} {
			data, err := os.ReadFile(filepath.Join(path, attribute))
			if err != nil {
				continue
			}
			if value, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && value > 0 {
				classes[id] = value
				break
			}
		}
	}
	return classes
}
