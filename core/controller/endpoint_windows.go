//go:build windows

package controller

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/UruhaLushia/sparkle-service/core/security"
	"github.com/UruhaLushia/sparkle-service/listen/namedpipe"

	"golang.org/x/sys/windows"
)

func EndpointReachable(network, address string) bool {
	if network != "pipe" {
		return false
	}
	conn, err := namedpipe.DialTimeout(address, 500*time.Millisecond)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func CreatePrivateEndpoint() (string, string, func(), error) {
	token, err := randomToken(16)
	if err != nil {
		return "", "", nil, err
	}
	return "pipe", `\\.\pipe\sparkle\mihomo-core-` + token, nil, nil
}

func HardenEndpoint(network string, address string) error {
	if network != "pipe" {
		return nil
	}
	if err := security.RestrictPath(address, windows.NO_INHERITANCE); err != nil {
		return fmt.Errorf("加固核心控制器 pipe 权限失败：%w", err)
	}
	return nil
}

func randomToken(size int) (string, error) {
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("生成核心控制器 pipe token 失败：%w", err)
	}
	return hex.EncodeToString(data), nil
}
