package core

import (
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/UruhaLushia/sparkle-service/core/process"
	"github.com/UruhaLushia/sparkle-service/core/security"
	ps "github.com/shirou/gopsutil/v4/process"
)

func (cm *CoreManager) takeoverRestartedProcess() bool {
	deadline := time.Now().Add(takeoverGracePeriod)
	for time.Now().Before(deadline) {
		cm.mutex.Lock()
		if !cm.monitoring.Load() || !cm.isRunning.Load() || cm.controller == nil || cm.launch == nil {
			cm.mutex.Unlock()
			return false
		}
		oldPID, controller, launch := cm.pid.Load(), cm.controller, cm.launch
		cm.mutex.Unlock()

		newPID, ok := findManagedCorePID(controller, oldPID, launch)
		if ok {
			if err := security.SecureBinary(launch.sourcePath); err != nil {
				log.Printf("重新接管前加固核心文件失败: %v", err)
				_ = controller.Stop(newPID)
				return false
			}
			if err := hardenLaunchControllerEndpoint(launch); err != nil {
				log.Printf("重新接管前加固核心控制器 IPC 失败: %v", err)
				_ = controller.Stop(newPID)
				return false
			}

			cm.mutex.Lock()
			if cm.monitoring.Load() && cm.controller == controller {
				cm.cmd = nil
				cm.pid.Store(newPID)
				cm.updateStartTimeFromPIDLocked(newPID)
				cm.startPIDPollingLocked(cm.stopChan)
				cm.mutex.Unlock()
				if err := writeRuntimeRecord(newPID, launch.executablePath); err != nil {
					log.Printf("更新接管后的核心运行记录失败: %v", err)
				}
				log.Printf("核心进程已重新接管 (PID: %d -> %d)", oldPID, newPID)
				cm.publishCoreEvent(cm.newCoreEvent(CoreEventTakeover, "核心进程已重新接管", nil, newPID, oldPID))
				return true
			}
			cm.mutex.Unlock()
			return false
		}
		time.Sleep(takeoverCheckInterval)
	}
	return false
}

func findManagedCorePID(controller process.Controller, oldPID int32, launch *launchSession) (int32, bool) {
	pids, err := controller.PIDs()
	if err != nil {
		log.Printf("查询核心进程组失败: %v", err)
		return 0, false
	}
	var bestPID int32
	var bestCreateTime int64
	for _, pid := range pids {
		if pid <= 0 || pid == oldPID || !isCoreProcessCandidate(pid, launch) {
			continue
		}
		createTime := int64(0)
		if proc, err := ps.NewProcess(pid); err == nil {
			if value, err := proc.CreateTime(); err == nil {
				createTime = value
			}
		}
		if bestPID == 0 || createTime >= bestCreateTime {
			bestPID, bestCreateTime = pid, createTime
		}
	}
	return bestPID, bestPID != 0
}

func isCoreProcessCandidate(pid int32, launch *launchSession) bool {
	if launch == nil {
		return false
	}
	proc, err := ps.NewProcess(pid)
	if err != nil {
		return false
	}
	expectedName := strings.ToLower(filepath.Base(launch.executablePath))
	if name, err := proc.Name(); err == nil && strings.ToLower(name) == expectedName {
		return true
	}
	exe, err := proc.Exe()
	return err == nil && strings.EqualFold(exe, launch.executablePath)
}

func (cm *CoreManager) updateStartTimeFromPIDLocked(pid int32) {
	proc, err := ps.NewProcess(pid)
	if err != nil {
		cm.startTime = time.Now()
		return
	}
	createTime, err := proc.CreateTime()
	if err != nil {
		cm.startTime = time.Now()
		return
	}
	cm.startTime = time.UnixMilli(createTime)
}
