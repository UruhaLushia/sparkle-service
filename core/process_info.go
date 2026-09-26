package core

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/process"
)

func (cm *CoreManager) IsHealthy() bool {
	if !cm.isRunning.Load() {
		return false
	}

	info, err := cm.GetProcessInfo()
	if err != nil {
		return false
	}
	if info.Memory > 1024*1024*1024 {
		log.Printf("警告: 核心进程内存使用过高 (%s)", info.MemoryFormat)
	}
	return true
}

func (cm *CoreManager) GetProcessInfo() (*ProcessInfo, error) {
	cm.mutex.Lock()
	pid := cm.pid.Load()
	startTime := cm.startTime
	launch := cm.launch
	cm.mutex.Unlock()

	if !cm.isRunning.Load() || pid <= 0 {
		return nil, fmt.Errorf("进程未运行")
	}

	proc, err := process.NewProcess(pid)
	if err != nil {
		return nil, fmt.Errorf("获取进程信息失败：%w", err)
	}

	info := &ProcessInfo{PID: pid, StartTime: startTime, Uptime: formatUptime(time.Since(startTime))}
	if launch != nil {
		info.LaunchMode = "managed"
		info.Executable = launch.sourcePath
	}
	if memInfo, err := proc.MemoryInfo(); err == nil {
		info.Memory = memInfo.RSS
		info.MemoryFormat = formatMemory(memInfo.RSS)
	}
	if cpuPercent, err := proc.CPUPercent(); err == nil {
		info.CPUPercent = cpuPercent
	}
	if affinity, err := proc.CPUAffinity(); err == nil {
		info.CPUAffinity = make([]int, 0, len(affinity))
		for _, cpu := range affinity {
			info.CPUAffinity = append(info.CPUAffinity, int(cpu))
		}
	}
	return info, nil
}

func formatMemory(bytes uint64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.2f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.2f MB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.2f KB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

func formatUptime(d time.Duration) string {
	days, hours := int(d.Hours())/24, int(d.Hours())%24
	minutes, seconds := int(d.Minutes())%60, int(d.Seconds())%60
	parts := make([]string, 0, 4)
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", days))
	}
	if hours > 0 || len(parts) > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if minutes > 0 || len(parts) > 0 {
		parts = append(parts, fmt.Sprintf("%dm", minutes))
	}
	parts = append(parts, fmt.Sprintf("%ds", seconds))
	return strings.Join(parts, " ")
}
