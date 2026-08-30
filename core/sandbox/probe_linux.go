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
	command, err := NewCommand(Config{
		ExecutablePath: executable,
		Args:           []string{"--help"},
		WorkingDir:     filepath.Dir(executable),
	})
	if err != nil {
		return err
	}

	if err := command.Cmd.Start(); err != nil {
		_ = command.Cleanup()
		return fmt.Errorf("chroot 或 namespace 探测失败：%w", err)
	}
	if err := command.AwaitExec(); err != nil {
		_ = command.Cmd.Process.Kill()
		_ = command.Cmd.Wait()
		_ = command.Cleanup()
		return fmt.Errorf("沙盒 re-exec 探测失败：%w", err)
	}
	if err := command.Cmd.Wait(); err != nil {
		_ = command.Cleanup()
		return fmt.Errorf("chroot 或 namespace 探测失败：%w", err)
	}
	return command.Cleanup()
}
