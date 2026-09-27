//go:build !windows

package controller

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

func EndpointReachable(network, address string) bool {
	if network != "unix" {
		return false
	}
	conn, err := net.DialTimeout("unix", address, 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func CreatePrivateEndpoint() (string, string, func(), error) {
	dir, err := os.MkdirTemp("", "sparkle-mihomo-controller-*")
	if err != nil {
		return "", "", nil, fmt.Errorf("创建核心控制器目录失败：%w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return "", "", nil, fmt.Errorf("设置核心控制器目录权限失败：%w", err)
	}

	address := filepath.Join(dir, "controller.sock")
	cleanup := func() {
		_ = os.RemoveAll(dir)
	}
	return "unix", address, cleanup, nil
}

func HardenEndpoint(network string, address string) error {
	if network != "unix" {
		return nil
	}

	dir := filepath.Dir(address)
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("加固核心控制器目录权限失败：%w", err)
	}
	if err := os.Chmod(address, 0o600); err != nil {
		return fmt.Errorf("加固核心控制器 UDS 权限失败：%w", err)
	}
	return nil
}
