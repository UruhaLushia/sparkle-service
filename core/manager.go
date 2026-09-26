package core

import (
	"errors"
	"fmt"
	"log"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/UruhaLushia/sparkle-service/core/process"
	"github.com/UruhaLushia/sparkle-service/core/security"
)

const (
	startTimeout          = 30 * time.Second
	fatalIndicator        = "level=fatal"
	monitorInterval       = 1 * time.Second
	takeoverGracePeriod   = 10 * time.Second
	takeoverCheckInterval = 250 * time.Millisecond
	maxCrashRestarts      = 3
	maxRestartBackoff     = 30 * time.Second
	startupBufferLimit    = 128 * 1024
	startupLineLimit      = 16 * 1024
)

type CoreManager struct {
	cmd                    *exec.Cmd
	controller             process.Controller
	launch                 *launchSession
	eventHub               coreEventHub
	isRunning              atomic.Bool
	monitoring             atomic.Bool
	pidPolling             atomic.Bool
	startTime              time.Time
	pid                    atomic.Int32
	mutex                  sync.Mutex
	stopChan               chan struct{}
	restartAttempts        int
	trafficMonitorPipeSDDL string
}

func (s *launchSession) addCleanup(cleanup func()) {
	if cleanup == nil {
		return
	}
	previous := s.cleanup
	s.cleanup = func() {
		cleanup()
		if previous != nil {
			previous()
		}
	}
}

type ProcessInfo struct {
	PID          int32     `json:"pid"`
	Memory       uint64    `json:"memory"`
	MemoryFormat string    `json:"memory_format"`
	CPUPercent   float64   `json:"cpu_percent"`
	CPUAffinity  []int     `json:"cpu_affinity,omitempty"`
	StartTime    time.Time `json:"start_time"`
	Uptime       string    `json:"uptime"`
	LaunchMode   string    `json:"launch_mode,omitempty"`
	Executable   string    `json:"executable,omitempty"`
}

type CoreManagerOption func(*CoreManager)

func WithTrafficMonitorPipeSDDL(sddl string) CoreManagerOption {
	return func(cm *CoreManager) {
		cm.trafficMonitorPipeSDDL = sddl
	}
}

func NewCoreManager(options ...CoreManagerOption) *CoreManager {
	cm := &CoreManager{}
	for _, option := range options {
		if option != nil {
			option(cm)
		}
	}
	return cm
}

func (cm *CoreManager) StartCore() error {
	return cm.StartCoreWithProfile(nil)
}

func (cm *CoreManager) StartCoreWithProfile(profile *LaunchProfile, options ...LaunchOption) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	return cm.startCoreLocked(profile, collectLaunchOptions(options))
}

func (cm *CoreManager) startCoreLocked(profile *LaunchProfile, options launchOptions) error {
	if !cm.isRunning.CompareAndSwap(false, true) {
		return fmt.Errorf("核心进程已在运行中")
	}
	cm.emitCoreEvent(CoreEventStarting, "核心正在启动", nil)

	cm.stopChan = make(chan struct{})

	if err := cm.startProcessLocked(profile, options); err != nil {
		return err
	}
	if cm.launch != nil {
		if err := persistDesiredState(true, cm.launch.profile); err != nil {
			log.Printf("保存核心运行状态失败: %v", err)
		}
	}
	return nil
}

func (cm *CoreManager) StopCore() error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	return cm.stopCoreLocked()
}

func (cm *CoreManager) stopCoreLocked() error {
	if cm.pid.Load() == 0 && cm.controller == nil && cm.launch == nil && !cm.isRunning.Load() {
		if err := clearDesiredState(); err != nil {
			log.Printf("保存核心停止状态失败: %v", err)
		}
		return nil
	}

	cm.emitCoreEvent(CoreEventStopping, "核心正在停止", nil)
	cm.monitoring.Store(false)
	cm.signalStopLocked()

	stopErr := cm.stopProcessLocked()
	cm.cleanupLocked()
	if err := clearDesiredState(); err != nil {
		log.Printf("保存核心停止状态失败: %v", err)
	}
	if stopErr != nil {
		return stopErr
	}
	cm.emitCoreEvent(CoreEventStopped, "核心已停止", nil)
	return nil
}

func (cm *CoreManager) RestartCore() error {
	return cm.RestartCoreWithProfile(nil)
}

func (cm *CoreManager) RestartCoreWithProfile(profile *LaunchProfile, options ...LaunchOption) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	cm.emitCoreEvent(CoreEventRestarting, "核心正在重启", nil)
	if err := cm.stopCoreLocked(); err != nil {
		log.Printf("停止进程时出错: %v", err)
	}

	time.Sleep(100 * time.Millisecond)
	return cm.startCoreLocked(profile, collectLaunchOptions(options))
}

func (cm *CoreManager) ApplyLaunchProfile(profile LaunchProfile, options ...LaunchOption) error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	if cm.launch == nil {
		return nil
	}
	if cm.isRunning.Load() && cm.pid.Load() > 0 && !slices.Equal(cm.launch.profile.CPUAffinity, profile.CPUAffinity) {
		if err := process.SetCPUAffinity(cm.pid.Load(), profile.CPUAffinity, cm.launch.defaultCPUAffinity); err != nil {
			return fmt.Errorf("动态更新核心 CPU 绑定失败：%w", err)
		}
	}

	launchOptions := collectLaunchOptions(options)
	access := cm.launch.fileAccess
	if launchOptions.fileAccess.ok {
		access = launchOptions.fileAccess
	}
	settings := coreLogSettingsFromProfile(profile, access)
	cm.launch.profile.LogPath = profile.LogPath
	cm.launch.profile.SaveLogs = profile.SaveLogs
	cm.launch.profile.MaxLogFileSizeMB = profile.MaxLogFileSizeMB
	cm.launch.profile.CPUAffinity = slices.Clone(profile.CPUAffinity)
	cm.launch.fileAccess = settings.access
	cm.launch.logPath = settings.path
	cm.launch.saveLogs = settings.saveLogs
	cm.launch.maxLogBytes = settings.maxBytes

	if cm.launch.logWriter != nil {
		cm.launch.logWriter.Update(settings)
	}
	return nil
}

func (cm *CoreManager) ControllerEndpoint() (string, string, error) {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	if cm.launch == nil || cm.launch.controllerNet == "" || cm.launch.controllerAddr == "" {
		return "", "", fmt.Errorf("核心控制器未初始化")
	}

	return cm.launch.controllerNet, cm.launch.controllerAddr, nil
}

func (cm *CoreManager) stopProcessLocked() error {
	pid := cm.pid.Load()
	if pid <= 0 || cm.controller == nil {
		return nil
	}
	return cm.controller.Stop(pid)
}

func (cm *CoreManager) cleanupLocked() {
	if cm.controller != nil {
		closeProcessController(cm.controller)
		cm.controller = nil
	}
	if cm.launch != nil {
		cm.launch.cleanupNow()
		cm.launch = nil
	}
	removeRuntimeRecord()

	cm.cmd = nil
	cm.startTime = time.Time{}
	cm.pid.Store(0)
	cm.isRunning.Store(false)
}

func closeProcessController(controller process.Controller) {
	if controller == nil {
		return
	}
	if err := controller.Close(); err != nil {
		log.Printf("关闭核心进程控制器失败: %v", err)
	}
}

func (cm *CoreManager) signalStopLocked() {
	if cm.stopChan != nil {
		close(cm.stopChan)
		cm.stopChan = nil
	}
}

func (cm *CoreManager) monitorProcess(cmd *exec.Cmd, errBuffer *boundedOutputBuffer, processDone <-chan error) {
	err := <-processDone
	if !cm.monitoring.Load() {
		return
	}

	cm.mutex.Lock()
	if cm.cmd != cmd {
		cm.mutex.Unlock()
		return
	}
	cm.mutex.Unlock()

	if err != nil {
		log.Printf("核心进程异常退出: %v\n错误输出: %s", err, errBuffer.String())
	} else {
		log.Printf("核心进程已退出 (PID: %d)", cmd.Process.Pid)
	}
	reason := processExitReason(err, errBuffer.String())
	crashed := reason == "panic" || reason == "signal"
	message := "核心进程已正常退出"
	switch reason {
	case "panic":
		message = "核心进程 panic 崩溃"
	case "signal":
		message = "核心进程被信号终止"
	case "exit_error":
		message = "核心进程错误退出，跳过自动重启"
	}
	cm.publishCoreEvent(cm.newCoreEvent(CoreEventExited, message, err, int32(cmd.Process.Pid), 0))
	if !crashed {
		if runtime.GOOS == "windows" && cm.takeoverRestartedProcess() {
			return
		}
		cm.finishExpectedProcessExit(cmd)
		return
	}

	cm.handleProcessExit()
}

func processExitReason(err error, output string) string {
	if strings.Contains(strings.ToLower(output), "panic:") {
		return "panic"
	}
	if err == nil {
		return "normal"
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return "abnormal"
	}
	if exitErr.ExitCode() == 0 {
		return "normal"
	}
	if exitErr.ExitCode() < 0 {
		return "signal"
	}
	return "exit_error"
}

func (cm *CoreManager) finishExpectedProcessExit(cmd *exec.Cmd) {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()
	if cm.cmd != cmd {
		return
	}
	cm.monitoring.Store(false)
	cm.signalStopLocked()
	cm.cleanupLocked()
	if err := clearDesiredState(); err != nil {
		log.Printf("保存核心正常退出状态失败: %v", err)
	}
}

func (cm *CoreManager) monitorStartupNotifications(launch *launchSession, stopChan <-chan struct{}) {
	for {
		select {
		case _, ok := <-launch.readyNotify:
			if !ok {
				return
			}
			cm.handleStartupNotification(launch)
		case <-stopChan:
			return
		}
	}
}

func (cm *CoreManager) handleStartupNotification(launch *launchSession) {
	cm.mutex.Lock()
	if !cm.monitoring.Load() || !cm.isRunning.Load() || cm.launch != launch {
		cm.mutex.Unlock()
		return
	}
	oldPID := cm.pid.Load()
	controller := cm.controller
	cm.mutex.Unlock()

	if err := security.SecureBinary(launch.sourcePath); err != nil {
		log.Printf("核心启动通知后加固核心文件失败: %v", err)
	}
	if err := hardenLaunchControllerEndpoint(launch); err != nil {
		log.Printf("核心启动通知后加固核心控制器 IPC 失败: %v", err)
	}

	newPID := oldPID
	if controller != nil {
		if pid, ok := findManagedCorePID(controller, oldPID, launch); ok {
			newPID = pid
		}
	}

	cm.mutex.Lock()
	if !cm.monitoring.Load() || cm.launch != launch || cm.controller != controller {
		cm.mutex.Unlock()
		return
	}
	if newPID != oldPID {
		cm.cmd = nil
		cm.pid.Store(newPID)
		cm.updateStartTimeFromPIDLocked(newPID)
		cm.startPIDPollingLocked(cm.stopChan)
	}
	cm.mutex.Unlock()

	if newPID != oldPID {
		log.Printf("核心进程已通过启动通知重新接管 (PID: %d -> %d)", oldPID, newPID)
		cm.publishCoreEvent(cm.newCoreEvent(CoreEventTakeover, "核心进程已重新接管", nil, newPID, oldPID))
		return
	}
	cm.publishCoreEvent(cm.newCoreEvent(CoreEventReady, "核心已重新就绪", nil, newPID, 0))
}

func (cm *CoreManager) handleProcessExit() {
	if cm.takeoverRestartedProcess() {
		return
	}

	cm.mutex.Lock()

	if cm.pid.Load() == 0 && cm.controller == nil && cm.launch == nil && !cm.isRunning.Load() {
		cm.mutex.Unlock()
		return
	}

	profile := LaunchProfile{}
	access := fileAccess{}
	if cm.launch != nil {
		profile = cm.launch.profile
		access = cm.launch.fileAccess
	}
	cm.restartAttempts++
	if cm.restartAttempts > maxCrashRestarts {
		cm.emitCoreEvent(CoreEventRestartFailed, "核心崩溃重启次数达到上限，已停止自动恢复", nil)
		cm.monitoring.Store(false)
		cm.signalStopLocked()
		cm.cleanupLocked()
		cm.mutex.Unlock()
		log.Printf("核心崩溃重启次数达到上限，停止自动恢复")
		return
	}
	cm.emitCoreEvent(CoreEventRestarting, "核心异常退出，正在重启", nil)
	cm.monitoring.Store(false)
	cm.signalStopLocked()
	cm.cleanupLocked()
	cm.mutex.Unlock()

	go func() {
		for retries := range 3 {
			if retries > 0 {
				time.Sleep(restartBackoff(retries))
			}
			if err := cm.StartCoreWithProfile(&profile, withFileAccess(access)); err != nil {
				log.Printf("重启核心进程失败 (尝试 %d/3): %v", retries+1, err)
				continue
			}
			log.Println("核心进程已成功重启")
			return
		}
		err := fmt.Errorf("达到最大重试次数，重启失败")
		cm.emitCoreEvent(CoreEventRestartFailed, "核心重启失败", err)
		log.Println(err)
	}()
}

func restartBackoff(attempt int) time.Duration {
	if attempt <= 0 {
		return 0
	}
	delay := time.Second << min(attempt-1, 5)
	if delay > maxRestartBackoff {
		return maxRestartBackoff
	}
	return delay
}

func (cm *CoreManager) startPIDPollingLocked(stopChan <-chan struct{}) {
	if stopChan == nil || !cm.pidPolling.CompareAndSwap(false, true) {
		return
	}
	go cm.monitorPID(stopChan)
}

func (cm *CoreManager) monitorPID(stopChan <-chan struct{}) {
	defer cm.pidPolling.Store(false)

	ticker := time.NewTicker(monitorInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if !cm.monitoring.Load() {
				return
			}

			pid := cm.pid.Load()
			if pid <= 0 {
				continue
			}

			exists, err := process.Exists(pid)
			if err != nil {
				log.Printf("检查核心进程失败: %v", err)
				continue
			}
			if !exists && cm.isRunning.Load() {
				log.Printf("核心进程已终止 (PID: %d)", pid)
				cm.handleProcessExit()
			}
		case <-stopChan:
			return
		}
	}
}
