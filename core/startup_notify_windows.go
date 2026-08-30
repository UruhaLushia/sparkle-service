//go:build windows

package core

import (
	"fmt"
	"time"

	"github.com/UruhaLushia/sparkle-service/core/security"
	"github.com/UruhaLushia/sparkle-service/listen"
	"github.com/UruhaLushia/sparkle-service/listen/namedpipe"
)

func createNativeStartupHook(token string) (*coreStartupHook, error) {
	pipePath := `\\.\pipe\sparkle\core-notify-` + token
	listener, err := listen.ListenNamedPipe(pipePath, currentProcessPipeSDDL())
	if err != nil {
		return nil, fmt.Errorf("创建核心启动通知管道失败：%w", err)
	}
	postUpCommand, err := startupNotifyCommand()
	if err != nil {
		_ = listener.Close()
		return nil, err
	}

	waitNotification := func() (bool, error) {
		conn, err := listener.Accept()
		if err != nil {
			return true, err
		}
		return false, readStartupNotification(conn, token)
	}
	return newCoreStartupHook(waitNotification, pipePath, postUpCommand, noopShellCommand(), startupNotificationEnv("pipe", pipePath, token), func() {
		_ = listener.Close()
	}), nil
}

func sendNativeStartupNotification(network string, address string, token string) error {
	if network != "pipe" {
		return fmt.Errorf("windows 启动通知仅支持 pipe")
	}
	conn, err := namedpipe.DialTimeout(address, 5*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write([]byte(token))
	return err
}

func currentProcessPipeSDDL() string {
	sid, err := security.CurrentProcessSID()
	if err != nil {
		return "D:P(A;;GA;;;SY)(A;;GA;;;BA)"
	}
	return fmt.Sprintf("D:P(A;;GA;;;%s)(A;;GA;;;SY)(A;;GA;;;BA)", sid.String())
}
