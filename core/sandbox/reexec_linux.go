//go:build linux

package sandbox

import (
	"encoding/json"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func init() {
	if os.Getenv(reexecModeEnv) != "1" {
		return
	}
	if err := runReexec(); err != nil {
		reportReexecError(err)
		os.Exit(125)
	}
	os.Exit(0)
}

func runReexec() error {
	configFile := os.NewFile(3, "sandbox-config")
	if configFile == nil {
		return fmt.Errorf("核心沙盒 re-exec 配置描述符无效")
	}
	var reexec reexecConfig
	decoder := json.NewDecoder(configFile)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&reexec); err != nil {
		_ = configFile.Close()
		return fmt.Errorf("读取核心沙盒 re-exec 配置失败：%w", err)
	}
	if err := configFile.Close(); err != nil {
		return fmt.Errorf("关闭核心沙盒 re-exec 配置失败：%w", err)
	}

	unix.CloseOnExec(4)
	if err := prepareRoot(reexec.Config, reexec.Root); err != nil {
		return err
	}
	if err := syscall.Chroot(reexec.Root); err != nil {
		_ = cleanupLinuxSandboxRoot(reexec.Root)
		return fmt.Errorf("进入核心沙盒失败：%w", err)
	}
	if err := os.Chdir(reexec.Config.WorkingDir); err != nil {
		return fmt.Errorf("切换核心工作目录失败 %s：%w", reexec.Config.WorkingDir, err)
	}

	args := append([]string{reexec.Config.ExecutablePath}, reexec.Config.Args...)
	return syscall.Exec(reexec.Config.ExecutablePath, args, reexec.Config.Env)
}

func reportReexecError(err error) {
	message := fmt.Sprintf("核心沙盒 re-exec 失败：%v", err)
	statusFile := os.NewFile(4, "sandbox-status")
	if statusFile != nil {
		_, _ = statusFile.WriteString(message)
		_ = statusFile.Close()
	}
	_, _ = fmt.Fprintln(os.Stderr, message)
}
