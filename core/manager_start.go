package core

import (
	"fmt"
	"io"
	"log"
	"time"
)

func (cm *CoreManager) startProcessLocked(profile *LaunchProfile, options launchOptions) error {
	errBuffer := newBoundedOutputBuffer(startupBufferLimit)
	startupWatcher := newStartupLogWatcher()

	launch, err := cm.prepareLaunchSession(profile, options)
	if err != nil {
		cm.monitoring.Store(false)
		cm.signalStopLocked()
		cm.isRunning.Store(false)
		cm.emitCoreEvent(CoreEventFailed, "核心启动失败", err)
		return err
	}

	logWriter := newBoundedLogWriter(coreLogSettings{
		path:     launch.logPath,
		saveLogs: launch.saveLogs,
		maxBytes: launch.maxLogBytes,
		access:   launch.fileAccess,
	})
	launch.logWriter = logWriter

	controller := newProcessController()
	command, err := newCoreLauncher(launch).Command(launch)
	if err != nil {
		if closeErr := logWriter.Close(); closeErr != nil {
			log.Printf("关闭核心日志文件失败: %v", closeErr)
		}
		closeProcessController(controller)
		launch.cleanupNow()
		cm.monitoring.Store(false)
		cm.signalStopLocked()
		cm.isRunning.Store(false)
		cm.emitCoreEvent(CoreEventFailed, "核心启动失败", err)
		return err
	}
	launch.addCleanup(command.cleanupNow)
	launch.addCleanup(func() {
		if err := logWriter.Close(); err != nil {
			log.Printf("关闭核心日志文件失败: %v", err)
		}
	})
	logEventWatcher := newCoreLogEventWatcher(cm)
	launch.addCleanup(logEventWatcher.Stop)
	cmd := command.cmd
	cmd.Stdout = io.MultiWriter(startupWatcher, logEventWatcher, logWriter)
	cmd.Stderr = io.MultiWriter(errBuffer, startupWatcher, logEventWatcher, logWriter)

	cmd, err = command.start()
	if err != nil {
		closeProcessController(controller)
		launch.cleanupNow()
		cm.monitoring.Store(false)
		cm.signalStopLocked()
		cm.isRunning.Store(false)
		startErr := fmt.Errorf("启动核心进程失败：%w", err)
		cm.emitCoreEvent(CoreEventFailed, "核心启动失败", startErr)
		return startErr
	}

	pid := int32(cmd.Process.Pid)
	if err := controller.Attach(pid); err != nil {
		_ = cmd.Process.Kill()
		closeProcessController(controller)
		launch.cleanupNow()
		cm.monitoring.Store(false)
		cm.signalStopLocked()
		cm.isRunning.Store(false)
		attachErr := fmt.Errorf("附加核心进程控制失败：%w", err)
		cm.emitCoreEvent(CoreEventFailed, "核心启动失败", attachErr)
		return attachErr
	}

	if err := setProcessPriority(pid, launch.cpuPriority); err != nil {
		log.Printf("设置核心进程优先级失败: %v", err)
	}

	cm.cmd = cmd
	cm.controller = controller
	cm.launch = launch
	cm.pid.Store(pid)
	cm.startTime = time.Now()

	processDone := make(chan error, 1)
	go func() {
		processDone <- cmd.Wait()
	}()

	if err := cm.waitForStartup(launch, errBuffer, startupWatcher.Fatal(), processDone); err != nil {
		_ = cm.stopProcessLocked()
		cm.monitoring.Store(false)
		cm.signalStopLocked()
		cm.cleanupLocked()
		cm.emitCoreEvent(CoreEventFailed, "核心启动失败", err)
		return err
	}
	if err := hardenLaunchControllerEndpoint(launch); err != nil {
		_ = cm.stopProcessLocked()
		cm.monitoring.Store(false)
		cm.signalStopLocked()
		cm.cleanupLocked()
		cm.emitCoreEvent(CoreEventFailed, "核心启动失败", err)
		return err
	}
	if cleanup, err := startTrafficMonitorProxy(launch, cm.trafficMonitorPipeSDDL); err != nil {
		log.Printf("启动 TrafficMonitor 兼容 pipe 失败: %v", err)
	} else {
		launch.addCleanup(cleanup)
	}
	cm.monitoring.Store(true)
	go cm.monitorProcess(cmd, errBuffer, processDone)
	if launch.readyNotify != nil {
		go cm.monitorStartupNotifications(launch, cm.stopChan)
	}
	cm.emitCoreEvent(CoreEventStarted, "核心已启动", nil)

	return nil
}
