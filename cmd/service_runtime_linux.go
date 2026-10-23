//go:build linux

package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

const (
	serviceRuntimeExecEnv = "SPARKLE_SERVICE_RUNTIME_EXEC"
	serviceRuntimeDir     = "/var/lib/sparkle/service"
)

func ensureServiceRuntimeExecutable() error {
	if os.Getenv(serviceRuntimeExecEnv) == "1" {
		return nil
	}

	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("读取服务可执行文件路径失败：%w", err)
	}

	target, err := stageServiceExecutable(executable)
	if err != nil {
		return err
	}

	env := append(os.Environ(), serviceRuntimeExecEnv+"=1")
	args := append([]string{target}, os.Args[1:]...)
	if err := syscall.Exec(target, args, env); err != nil {
		return fmt.Errorf("执行服务运行副本失败 %q：%w", target, err)
	}
	return nil
}

func stageServiceExecutable(source string) (string, error) {
	hash, mode, err := hashFile(source)
	if err != nil {
		return "", err
	}

	dir := filepath.Join(serviceRuntimeDir, hash)
	for _, path := range []string{filepath.Dir(serviceRuntimeDir), serviceRuntimeDir, dir} {
		if err := os.Mkdir(path, 0o755); err != nil && !os.IsExist(err) {
			return "", fmt.Errorf("创建服务运行目录失败 %q：%w", path, err)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return "", fmt.Errorf("检查服务运行目录失败 %q：%w", path, err)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() || stat.Uid != 0 || info.Mode().Perm()&0o022 != 0 {
			return "", fmt.Errorf("服务运行目录必须由 root 所有且不可由组或其他用户写入 %q", path)
		}
	}

	var stat unix.Statfs_t
	if err := unix.Statfs(dir, &stat); err != nil {
		return "", fmt.Errorf("检查服务运行目录挂载选项失败 %q：%w", dir, err)
	}
	if stat.Flags&unix.ST_NOEXEC != 0 {
		return "", fmt.Errorf("服务运行目录所在文件系统启用了 noexec，无法执行运行副本 %q", dir)
	}

	target := filepath.Join(dir, filepath.Base(source))
	if _, err := os.Stat(target); err == nil {
		return target, nil
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("检查服务运行副本失败 %q：%w", target, err)
	}

	tmp, err := os.CreateTemp(dir, ".sparkle-service-*")
	if err != nil {
		return "", fmt.Errorf("创建服务运行副本失败 %q：%w", dir, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	input, err := os.Open(source)
	if err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("打开服务二进制失败 %q：%w", source, err)
	}
	_, copyErr := io.Copy(tmp, input)
	closeInputErr := input.Close()
	chmodErr := tmp.Chmod(mode | 0o500)
	closeTmpErr := tmp.Close()

	switch {
	case copyErr != nil:
		return "", fmt.Errorf("复制服务二进制失败 %q -> %q：%w", source, target, copyErr)
	case closeInputErr != nil:
		return "", fmt.Errorf("关闭服务二进制失败 %q：%w", source, closeInputErr)
	case chmodErr != nil:
		return "", fmt.Errorf("设置服务运行副本权限失败 %q：%w", tmpPath, chmodErr)
	case closeTmpErr != nil:
		return "", fmt.Errorf("关闭服务运行副本失败 %q：%w", tmpPath, closeTmpErr)
	}

	if err := os.Rename(tmpPath, target); err != nil {
		return "", fmt.Errorf("发布服务运行副本失败 %q：%w", target, err)
	}
	return target, nil
}

func cleanupServiceRuntimeExecutable() error {
	if err := os.RemoveAll(serviceRuntimeDir); err != nil {
		return fmt.Errorf("清理服务运行副本失败 %q：%w", serviceRuntimeDir, err)
	}
	return nil
}

func hashFile(path string) (string, os.FileMode, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("打开服务二进制失败 %q：%w", path, err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return "", 0, fmt.Errorf("读取服务二进制信息失败 %q：%w", path, err)
	}
	if info.IsDir() {
		return "", 0, fmt.Errorf("服务路径指向目录而非可执行文件: %s", path)
	}

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", 0, fmt.Errorf("计算服务二进制摘要失败 %q：%w", path, err)
	}

	return hex.EncodeToString(hash.Sum(nil))[:16], info.Mode().Perm(), nil
}
