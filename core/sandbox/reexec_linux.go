//go:build linux

package sandbox

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
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
	if reexec.Root != "" {
		if err := prepareRoot(reexec.Config, reexec.Root); err != nil {
			return err
		}
		if err := syscall.Chroot(reexec.Root); err != nil {
			_ = cleanupLinuxSandboxRoot(reexec.Root)
			return fmt.Errorf("进入核心沙盒失败：%w", err)
		}
	}
	if err := os.Chdir(reexec.Config.WorkingDir); err != nil {
		return fmt.Errorf("切换核心工作目录失败 %s：%w", reexec.Config.WorkingDir, err)
	}

	args := append([]string{reexec.Config.ExecutablePath}, reexec.Config.Args...)
	if len(reexec.Config.CPUAffinity) > 0 {
		// Affinity is per-thread and survives exec. Keep this goroutine on the
		// same thread until exec replaces the helper with the core process.
		runtime.LockOSThread()
		if err := applyCPUAffinity(reexec.Config.CPUAffinity); err != nil {
			return err
		}
	}
	return syscall.Exec(reexec.Config.ExecutablePath, args, reexec.Config.Env)
}

func applyCPUAffinity(cpus []int) error {
	var requested unix.CPUSet
	for _, cpu := range cpus {
		if cpu < 0 || cpu >= 1024 {
			return fmt.Errorf("CPU 编号超出支持范围：%d", cpu)
		}
		requested.Set(cpu)
	}
	if err := unix.SchedSetaffinity(0, &requested); err != nil {
		return fmt.Errorf("绑定核心 CPU %v 失败：%w", cpus, err)
	}
	var actual unix.CPUSet
	if err := unix.SchedGetaffinity(0, &actual); err != nil {
		return fmt.Errorf("读取核心 CPU 绑定结果失败：%w", err)
	}
	if actual != requested {
		return fmt.Errorf("无法绑定全部指定 CPU %v，请检查 CPU 是否在线及 cpuset 限制", cpus)
	}
	return nil
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
