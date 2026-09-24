package sys

import (
	"fmt"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/shirou/gopsutil/v4/cpu"
)

type CPUInfo struct {
	ID         int     `json:"id"`
	CoreID     string  `json:"core_id,omitempty"`
	PhysicalID string  `json:"physical_id,omitempty"`
	ModelName  string  `json:"model_name,omitempty"`
	Mhz        float64 `json:"mhz,omitempty"`
	CoreType   string  `json:"core_type,omitempty"`
	Available  bool    `json:"available"`
}

type CPUInfoResponse struct {
	CPUs              []CPUInfo `json:"cpus"`
	LogicalCPUCount   int       `json:"logical_cpu_count"`
	AvailableCPUCount int       `json:"available_cpu_count"`
	AffinitySupported bool      `json:"affinity_supported"`
}

func GetCPUInfo() (CPUInfoResponse, error) {
	stats, err := cpu.Info()
	if err != nil {
		return CPUInfoResponse{}, fmt.Errorf("读取 CPU 信息失败：%w", err)
	}
	affinity, supported, err := currentProcessCPUAffinity()
	if err != nil {
		return CPUInfoResponse{}, fmt.Errorf("读取 service 可用 CPU 失败：%w", err)
	}
	available := make(map[int]struct{}, len(affinity))
	for _, id := range affinity {
		available[int(id)] = struct{}{}
	}

	result := CPUInfoResponse{CPUs: make([]CPUInfo, 0, len(stats)), AffinitySupported: supported}
	for _, stat := range stats {
		_, isAvailable := available[int(stat.CPU)]
		if !supported {
			isAvailable = true
		}
		result.CPUs = append(result.CPUs, CPUInfo{
			ID:         int(stat.CPU),
			CoreID:     stat.CoreID,
			PhysicalID: stat.PhysicalID,
			ModelName:  stat.ModelName,
			Mhz:        stat.Mhz,
			CoreType:   cpuCoreType(int(stat.CPU)),
			Available:  isAvailable,
		})
	}
	sort.Slice(result.CPUs, func(i, j int) bool { return result.CPUs[i].ID < result.CPUs[j].ID })
	result.LogicalCPUCount = len(result.CPUs)
	for _, item := range result.CPUs {
		if item.Available {
			result.AvailableCPUCount++
		}
	}
	return result, nil
}

func cpuCoreType(id int) string {
	if runtime.GOOS != "linux" {
		return ""
	}
	data, err := os.ReadFile("/sys/devices/system/cpu/cpu" + strconv.Itoa(id) + "/topology/core_type")
	if err != nil {
		return ""
	}
	switch strings.TrimSpace(string(data)) {
	case "1":
		return "performance"
	case "2":
		return "efficiency"
	default:
		return ""
	}
}
