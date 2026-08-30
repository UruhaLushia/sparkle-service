//go:build linux

package sandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

var (
	probeOnce sync.Once
	probeErr  error
)

func Probe() error {
	probeOnce.Do(func() {
		probeErr = runProbe()
	})
	return probeErr
}

func runProbe() error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("需要 root 权限")
	}

	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("读取 service 可执行文件路径失败：%w", err)
	}
	cmd, cleanup, err := Command(Config{
		ExecutablePath: executable,
		Args:           []string{"--help"},
		WorkingDir:     filepath.Dir(executable),
	})
	if err != nil {
		return err
	}
	defer func() {
		if cleanup != nil {
			_ = cleanup()
		}
	}()

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("chroot 或 namespace 探测失败：%w", err)
	}
	if err := cleanup(); err != nil {
		return err
	}
	cleanup = nil
	return nil
}
