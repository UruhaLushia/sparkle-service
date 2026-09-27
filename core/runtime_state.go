package core

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/UruhaLushia/sparkle-service/core/controller"
	"github.com/UruhaLushia/sparkle-service/core/process"
	ps "github.com/shirou/gopsutil/v4/process"
)

type runtimeRecord struct {
	PID            int32         `json:"pid"`
	StartTime      int64         `json:"start_time"`
	Executable     string        `json:"executable"`
	ControllerNet  string        `json:"controller_net"`
	ControllerAddr string        `json:"controller_addr"`
	Profile        LaunchProfile `json:"profile"`
}

func runtimeRecordPath() string {
	return filepath.Join(serviceConfigDir(), "sparkle", "core", "runtime.json")
}

func writeRuntimeRecord(pid int32, launch *launchSession) error {
	proc, err := ps.NewProcess(pid)
	if err != nil {
		return fmt.Errorf("读取核心进程失败：%w", err)
	}
	startTime, err := proc.CreateTime()
	if err != nil {
		return fmt.Errorf("读取核心启动时间失败：%w", err)
	}
	path := runtimeRecordPath()
	data, err := json.Marshal(runtimeRecord{PID: pid, StartTime: startTime, Executable: launch.executablePath, ControllerNet: launch.controllerNet, ControllerAddr: launch.controllerAddr, Profile: launch.profile})
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
	if record.ControllerNet != "" && record.ControllerAddr != "" && controller.EndpointReachable(record.ControllerNet, record.ControllerAddr) {
		if err := cm.adoptRuntimeRecord(record); err == nil {
			return nil
		}
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

func (cm *CoreManager) adoptRuntimeRecord(record runtimeRecord) error {
	controllerProcess := process.NewController()
	if err := controllerProcess.Attach(record.PID); err != nil {
		return err
	}
	defaultAffinity, err := process.CurrentCPUAffinity()
	if err != nil {
		defaultAffinity = nil
	}
	cm.mutex.Lock()
	cm.controller = controllerProcess
	cm.launch = &launchSession{sourcePath: record.Executable, executablePath: record.Executable, controllerNet: record.ControllerNet, controllerAddr: record.ControllerAddr, defaultCPUAffinity: defaultAffinity, profile: record.Profile}
	cm.cmd = nil
	cm.pid.Store(record.PID)
	cm.startTime = time.UnixMilli(record.StartTime)
	cm.stopChan = make(chan struct{})
	cm.isRunning.Store(true)
	cm.monitoring.Store(true)
	cm.startPIDPollingLocked(cm.stopChan)
	cm.mutex.Unlock()
	return nil
}
