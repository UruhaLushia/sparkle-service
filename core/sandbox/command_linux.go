//go:build linux

package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

const reexecModeEnv = "SPARKLE_CORE_SANDBOX_REEXEC"

type reexecConfig struct {
	Root   string `json:"root"`
	Config Config `json:"config"`
}

type Command struct {
	Cmd          *exec.Cmd
	root         string
	configFile   *os.File
	statusReader *os.File
	statusWriter *os.File
	awaitOnce    sync.Once
	awaitErr     error
	cleanupOnce  sync.Once
	cleanupErr   error
}

func NewCommand(config Config) (*Command, error) {
	if os.Geteuid() != 0 {
		return nil, fmt.Errorf("核心沙盒需要 root 权限")
	}
	if err := validateWritablePaths(config.WritablePaths); err != nil {
		return nil, err
	}

	root, err := createSandboxRoot()
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Command, error) {
		_ = cleanupLinuxSandboxRoot(root)
		return nil, err
	}

	configFile, err := createReexecConfigFile(root, reexecConfig{Root: root, Config: config})
	if err != nil {
		return fail(err)
	}
	statusReader, statusWriter, err := os.Pipe()
	if err != nil {
		_ = configFile.Close()
		return fail(fmt.Errorf("创建核心沙盒状态管道失败：%w", err))
	}

	serviceExecutable, err := os.Executable()
	if err != nil {
		_ = configFile.Close()
		_ = statusReader.Close()
		_ = statusWriter.Close()
		return fail(fmt.Errorf("读取 service 可执行文件路径失败：%w", err))
	}
	cmd := exec.Command(serviceExecutable)
	cmd.Env = reexecEnvironment()
	cmd.ExtraFiles = []*os.File{configFile, statusWriter}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:    true,
		Cloneflags: syscall.CLONE_NEWIPC | syscall.CLONE_NEWUTS | syscall.CLONE_NEWNS,
		Pdeathsig:  syscall.SIGKILL,
	}
	// Deliberately keep the host network namespace and root network capabilities:
	// TUN, routes, policy rules and netfilter must affect the host.

	return &Command{
		Cmd:          cmd,
		root:         root,
		configFile:   configFile,
		statusReader: statusReader,
		statusWriter: statusWriter,
	}, nil
}

func (c *Command) AwaitExec() error {
	if c == nil {
		return fmt.Errorf("核心沙盒命令为空")
	}
	c.awaitOnce.Do(func() {
		c.closeParentWriters()
		if c.statusReader == nil {
			c.awaitErr = fmt.Errorf("核心沙盒状态管道不可用")
			return
		}

		type readResult struct {
			data []byte
			err  error
		}
		result := make(chan readResult, 1)
		reader := c.statusReader
		go func() {
			data, err := io.ReadAll(io.LimitReader(reader, 64*1024))
			result <- readResult{data: data, err: err}
		}()

		timer := time.NewTimer(15 * time.Second)
		defer timer.Stop()
		select {
		case status := <-result:
			_ = reader.Close()
			c.statusReader = nil
			if status.err != nil {
				c.awaitErr = fmt.Errorf("读取核心沙盒启动状态失败：%w", status.err)
			} else if message := strings.TrimSpace(string(status.data)); message != "" {
				c.awaitErr = errors.New(message)
			}
		case <-timer.C:
			if c.Cmd != nil && c.Cmd.Process != nil {
				_ = c.Cmd.Process.Kill()
			}
			_ = reader.Close()
			c.statusReader = nil
			c.awaitErr = fmt.Errorf("等待核心沙盒 exec 超时")
		}
	})
	return c.awaitErr
}

func (c *Command) Cleanup() error {
	if c == nil {
		return nil
	}
	c.cleanupOnce.Do(func() {
		c.closeParentWriters()
		if c.statusReader != nil {
			_ = c.statusReader.Close()
			c.statusReader = nil
		}
		c.cleanupErr = cleanupLinuxSandboxRoot(c.root)
	})
	return c.cleanupErr
}

func (c *Command) closeParentWriters() {
	if c.configFile != nil {
		_ = c.configFile.Close()
		c.configFile = nil
	}
	if c.statusWriter != nil {
		_ = c.statusWriter.Close()
		c.statusWriter = nil
	}
}

func createReexecConfigFile(root string, config reexecConfig) (*os.File, error) {
	file, err := os.CreateTemp(root, ".reexec-config-*")
	if err != nil {
		return nil, fmt.Errorf("创建核心沙盒 re-exec 配置失败：%w", err)
	}
	name := file.Name()
	if err := os.Remove(name); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("隐藏核心沙盒 re-exec 配置失败：%w", err)
	}

	data, err := json.Marshal(config)
	if err == nil {
		_, err = file.Write(data)
	}
	if err == nil {
		_, err = file.Seek(0, io.SeekStart)
	}
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("写入核心沙盒 re-exec 配置失败：%w", err)
	}
	return file, nil
}

func reexecEnvironment() []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if key != reexecModeEnv {
			env = append(env, item)
		}
	}
	return append(env, reexecModeEnv+"=1")
}

func validateWritablePaths(paths []string) error {
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			return &configError{err: fmt.Errorf("映射可信路径失败：路径不能为空")}
		}
		path, err := normalizeSandboxPath(path)
		if err != nil {
			return &configError{err: fmt.Errorf("映射可信路径失败 %q：%w", path, err)}
		}
		if _, err := os.Stat(path); err != nil {
			return &configError{err: fmt.Errorf("映射可信路径失败 %q：%w", path, err)}
		}
	}
	return nil
}
