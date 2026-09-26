package core

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/UruhaLushia/sparkle-service/core/process"
	ps "github.com/shirou/gopsutil/v4/process"
)

type runtimeRecord struct {
	PID        int32  `json:"pid"`
	StartTime  int64  `json:"start_time"`
	Executable string `json:"executable"`
}

func runtimeRecordPath() string {
	return filepath.Join(serviceConfigDir(), "sparkle", "core", "runtime.json")
}

func writeRuntimeRecord(pid int32, executable string) error {
	proc, err := ps.NewProcess(pid)
	if err != nil {
		return fmt.Errorf("读取核心进程失败：%w", err)
	}
	startTime, err := proc.CreateTime()
	if err != nil {
		return fmt.Errorf("读取核心启动时间失败：%w", err)
	}
	path := runtimeRecordPath()
	data, err := json.Marshal(runtimeRecord{PID: pid, StartTime: startTime, Executable: executable})
	if err != nil {
		return err
	}
	return atomicWriteFile(path, data, 0o600)
}

func removeRuntimeRecord() {
	if err := os.Remove(runtimeRecordPath()); err != nil && !os.IsNotExist(err) {
		log.Printf("删除核心运行记录失败: %v", err)
	}
}

func (cm *CoreManager) ReconcileRuntimeState() error {
	path := runtimeRecordPath()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var record runtimeRecord
	if err := json.Unmarshal(data, &record); err != nil {
		removeRuntimeRecord()
		return nil
	}
	proc, err := ps.NewProcess(record.PID)
	if err != nil {
		removeRuntimeRecord()
		return nil
	}
	createTime, createErr := proc.CreateTime()
	executable, exeErr := proc.Exe()
	if createErr != nil || exeErr != nil || createTime != record.StartTime || executable != record.Executable {
		removeRuntimeRecord()
		return nil
	}
	if err := process.Terminate(record.PID); err != nil {
		return fmt.Errorf("清理残留核心进程失败：%w", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		exists, err := process.Exists(record.PID)
		if err == nil && !exists {
			removeRuntimeRecord()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("等待残留核心进程退出超时：PID %d", record.PID)
}
