//go:build windows

package core

import (
	"fmt"
	"os"
)

const startupNotifyExecutableEnv = "SPARKLE_CORE_STARTUP_NOTIFY_EXECUTABLE"

func startupNotifyCommand() (string, string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", "", fmt.Errorf("读取 service 可执行文件路径失败：%w", err)
	}
	return "%" + startupNotifyExecutableEnv + "%", `"` + executable + `"`, nil
}
