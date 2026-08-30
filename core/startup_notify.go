package core

import (
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"
)

const (
	startupNotifyModeEnv    = "SPARKLE_CORE_STARTUP_NOTIFY"
	startupNotifyNetworkEnv = "SPARKLE_CORE_STARTUP_NOTIFY_NETWORK"
	startupNotifyAddressEnv = "SPARKLE_CORE_STARTUP_NOTIFY_ADDRESS"
	startupNotifyTokenEnv   = "SPARKLE_CORE_STARTUP_NOTIFY_TOKEN"
)

func init() {
	if os.Getenv(startupNotifyModeEnv) != "1" {
		return
	}
	err := sendNativeStartupNotification(
		os.Getenv(startupNotifyNetworkEnv),
		os.Getenv(startupNotifyAddressEnv),
		os.Getenv(startupNotifyTokenEnv),
	)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "核心启动通知失败：%v\n", err)
		os.Exit(125)
	}
	os.Exit(0)
}

func startupNotificationEnv(network string, address string, token string) map[string]string {
	return map[string]string{
		startupNotifyModeEnv:    "1",
		startupNotifyNetworkEnv: network,
		startupNotifyAddressEnv: address,
		startupNotifyTokenEnv:   token,
	}
}

func readStartupNotification(conn net.Conn, token string) error {
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	data, err := io.ReadAll(io.LimitReader(conn, int64(len(token)+16)))
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(data)) != token {
		return fmt.Errorf("核心启动通知 token 不匹配")
	}
	return nil
}
