package core

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/UruhaLushia/sparkle-service/core/controller"
	"github.com/UruhaLushia/sparkle-service/core/security"

	"github.com/shirou/gopsutil/v4/process"
)

const (
	startTimeout          = 30 * time.Second
	fatalIndicator        = "level=fatal"
	monitorInterval       = 1 * time.Second
	takeoverGracePeriod   = 10 * time.Second
	takeoverCheckInterval = 250 * time.Millisecond
	startupBufferLimit    = 128 * 1024
	startupLineLimit      = 16 * 1024
)

type processController interface {
	Attach(pid int32) error
	PIDs() ([]int32, error)
	Stop(pid int32) error
	Close() error
}

type CoreManager struct {
	cmd                    *exec.Cmd
	controller             processController
	launch                 *launchSession
	eventHub               coreEventHub
	isRunning              atomic.Bool
	monitoring             atomic.Bool
	pidPolling             atomic.Bool
	startTime              time.Time
	pid                    atomic.Int32
	mutex                  sync.Mutex
	stopChan               chan struct{}
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

	return cm.startProcessLocked(profile, options)
}

func (cm *CoreManager) StopCore() error {
	cm.mutex.Lock()
	defer cm.mutex.Unlock()

	return cm.stopCoreLocked()
}

func (cm *CoreManager) stopCoreLocked() error {
	if cm.pid.Load() == 0 && cm.controller == nil && cm.launch == nil && !cm.isRunning.Load() {
		return nil
	}

	cm.emitCoreEvent(CoreEventStopping, "核心正在停止", nil)
	cm.monitoring.Store(false)
	cm.signalStopLocked()

	stopErr := cm.stopProcessLocked()
	cm.cleanupLocked()
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
		if err := setProcessCPUAffinity(cm.pid.Load(), profile.CPUAffinity, cm.launch.defaultCPUAffinity); err != nil {
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

	cm.cmd = nil
	cm.startTime = time.Time{}
	cm.pid.Store(0)
	cm.isRunning.Store(false)
}

func closeProcessController(controller processController) {
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
	cm.publishCoreEvent(cm.newCoreEvent(CoreEventExited, "核心进程已退出", err, int32(cmd.Process.Pid), 0))

	cm.handleProcessExit()
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
	cm.emitCoreEvent(CoreEventRestarting, "核心异常退出，正在重启", nil)
	cm.monitoring.Store(false)
	cm.signalStopLocked()
	cm.cleanupLocked()
	cm.mutex.Unlock()

	go func() {
		for retries := range 3 {
			if err := cm.StartCoreWithProfile(&profile, withFileAccess(access)); err != nil {
				log.Printf("重启核心进程失败 (尝试 %d/3): %v", retries+1, err)
				time.Sleep(time.Second * time.Duration(retries+1))
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

func (cm *CoreManager) takeoverRestartedProcess() bool {
	deadline := time.Now().Add(takeoverGracePeriod)

	for time.Now().Before(deadline) {
		cm.mutex.Lock()
		if !cm.monitoring.Load() || !cm.isRunning.Load() || cm.controller == nil || cm.launch == nil {
			cm.mutex.Unlock()
			return false
		}

		oldPID := cm.pid.Load()
		controller := cm.controller
		launch := cm.launch
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

func findManagedCorePID(controller processController, oldPID int32, launch *launchSession) (int32, bool) {
	pids, err := controller.PIDs()
	if err != nil {
		log.Printf("查询核心进程组失败: %v", err)
		return 0, false
	}

	var bestPID int32
	var bestCreateTime int64
	for _, pid := range pids {
		if pid <= 0 || pid == oldPID {
			continue
		}
		if !isCoreProcessCandidate(pid, launch) {
			continue
		}

		createTime := int64(0)
		if proc, err := process.NewProcess(pid); err == nil {
			if value, err := proc.CreateTime(); err == nil {
				createTime = value
			}
		}
		if bestPID == 0 || createTime >= bestCreateTime {
			bestPID = pid
			bestCreateTime = createTime
		}
	}

	return bestPID, bestPID != 0
}

func isCoreProcessCandidate(pid int32, launch *launchSession) bool {
	if launch == nil {
		return false
	}

	proc, err := process.NewProcess(pid)
	if err != nil {
		return false
	}

	expectedName := strings.ToLower(filepath.Base(launch.executablePath))
	if name, err := proc.Name(); err == nil && strings.ToLower(name) == expectedName {
		return true
	}

	if exe, err := proc.Exe(); err == nil && strings.EqualFold(exe, launch.executablePath) {
		return true
	}

	return false
}

func (cm *CoreManager) updateStartTimeFromPIDLocked(pid int32) {
	proc, err := process.NewProcess(pid)
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

			exists, err := process.PidExists(pid)
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

func (cm *CoreManager) waitForStartup(launch *launchSession, errBuffer *boundedOutputBuffer, startupFatal <-chan error, processDone <-chan error) error {
	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()

	if launch.waitReady == nil {
		return fmt.Errorf("核心启动通知未初始化")
	}

	ready := make(chan error, 1)
	go func() {
		ready <- launch.waitReady(ctx)
	}()

	for {
		select {
		case err := <-ready:
			if err != nil {
				return fmt.Errorf("等待核心 post-up 通知失败：%w", err)
			}
			return nil
		case err := <-startupFatal:
			if err != nil {
				return err
			}
		case err := <-processDone:
			if err != nil {
				return fmt.Errorf("核心进程启动前退出：%w，错误输出: %s", err, errBuffer.String())
			}
			return fmt.Errorf("核心进程启动前退出")
		case <-ctx.Done():
			return fmt.Errorf("启动核心进程超时")
		}
	}
}

func hardenLaunchControllerEndpoint(launch *launchSession) error {
	if launch == nil {
		return nil
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		err := controller.HardenEndpoint(launch.controllerNet, launch.controllerAddr)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func startupFatalLineError(line string) error {
	lower := strings.ToLower(line)
	switch {
	case strings.Contains(lower, fatalIndicator):
		return extractFatalError(line)
	case strings.Contains(line, "External controller pipe listen error"),
		strings.Contains(line, "External controller unix listen error"),
		strings.Contains(line, "External controller listen error"):
		return fmt.Errorf("控制器监听失败：%s", strings.TrimSpace(line))
	case strings.Contains(line, "Start TUN listening error"):
		return fmt.Errorf("虚拟网卡启动失败：%s", strings.TrimSpace(line))
	default:
		return nil
	}
}

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

	info := &ProcessInfo{
		PID:       pid,
		StartTime: startTime,
		Uptime:    formatUptime(time.Since(startTime)),
	}
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
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	seconds := int(d.Seconds()) % 60

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

func extractFatalError(output string) error {
	if _, after, ok := strings.Cut(output, "level=fatal msg="); ok {
		msg := strings.TrimSpace(after)
		return fmt.Errorf("启动核心进程失败: %s", msg)
	}
	return fmt.Errorf("启动核心进程失败：发现致命错误")
}
