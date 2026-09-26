package core

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/UruhaLushia/sparkle-service/core/controller"
)

func (cm *CoreManager) waitForStartup(launch *launchSession, errBuffer *boundedOutputBuffer, startupFatal <-chan error, processDone <-chan error) error {
	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()
	if launch.waitReady == nil {
		return fmt.Errorf("核心启动通知未初始化")
	}
	ready := make(chan error, 1)
	go func() { ready <- launch.waitReady(ctx) }()
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
		if err := controller.HardenEndpoint(launch.controllerNet, launch.controllerAddr); err == nil {
			return nil
		} else if time.Now().After(deadline) {
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
	case strings.Contains(line, "External controller pipe listen error"), strings.Contains(line, "External controller unix listen error"), strings.Contains(line, "External controller listen error"):
		return fmt.Errorf("控制器监听失败：%s", strings.TrimSpace(line))
	case strings.Contains(line, "Start TUN listening error"):
		return fmt.Errorf("虚拟网卡启动失败：%s", strings.TrimSpace(line))
	default:
		return nil
	}
}

func extractFatalError(output string) error {
	if _, after, ok := strings.Cut(output, "level=fatal msg="); ok {
		return fmt.Errorf("启动核心进程失败: %s", strings.TrimSpace(after))
	}
	return fmt.Errorf("启动核心进程失败：发现致命错误")
}
