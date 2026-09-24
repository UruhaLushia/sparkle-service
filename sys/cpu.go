package sys

import (
	"fmt"
	"sort"

	"github.com/shirou/gopsutil/v4/cpu"
)

type CPUInfo struct {
	ID         int     `json:"id"`
	CoreID     string  `json:"core_id,omitempty"`
	PhysicalID string  `json:"physical_id,omitempty"`
	ModelName  string  `json:"model_name,omitempty"`
	Mhz        float64 `json:"mhz,omitempty"`
	CoreClass  int     `json:"core_class"`
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
	coreClasses := cpuCoreClasses()
	for _, stat := range stats {
		_, isAvailable := available[int(stat.CPU)]
		if !supported {
			isAvailable = true
		}
		item := CPUInfo{
			ID:         int(stat.CPU),
			CoreID:     stat.CoreID,
			PhysicalID: stat.PhysicalID,
			ModelName:  stat.ModelName,
			Mhz:        stat.Mhz,
			CoreClass:  coreClasses[int(stat.CPU)],
			Available:  isAvailable,
		}
		result.CPUs = append(result.CPUs, item)
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
