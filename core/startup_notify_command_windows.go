//go:build windows

package core

import (
	"fmt"
	"os"
	"strings"
)

func startupNotifyCommand() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("读取 service 可执行文件路径失败：%w", err)
	}
	return `"` + strings.ReplaceAll(executable, `"`, `""`) + `"`, nil
}
