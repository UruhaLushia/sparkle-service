package core

import (
	"fmt"
	"os/exec"
)

type coreLauncher interface {
	Command(*launchSession) (*coreCommand, error)
}

type directCoreLauncher struct{}

type coreCommand struct {
	cmd           *exec.Cmd
	cleanup       func()
	startFallback func(error) (*coreCommand, error)
}

func newCoreCommand(cmd *exec.Cmd, cleanup func()) *coreCommand {
	return &coreCommand{cmd: cmd, cleanup: cleanup}
}

func (c *coreCommand) cleanupNow() {
	if c == nil || c.cleanup == nil {
		return
	}
	c.cleanup()
	c.cleanup = nil
}

func (c *coreCommand) start() (*exec.Cmd, error) {
	if c == nil || c.cmd == nil {
		return nil, fmt.Errorf("核心启动命令为空")
	}

	firstCmd := c.cmd
	if err := firstCmd.Start(); err == nil {
		return firstCmd, nil
	} else if c.startFallback == nil {
		c.cleanupNow()
		return nil, err
	} else {
		firstErr := err
		c.cleanupNow()
		fallback, fallbackErr := c.startFallback(firstErr)
		c.startFallback = nil
		if fallbackErr != nil {
			return nil, fmt.Errorf("沙盒启动失败：%v；准备直接启动失败：%w", firstErr, fallbackErr)
		}

		copyCommandIO(fallback.cmd, firstCmd)
		c.cmd = fallback.cmd
		c.cleanup = fallback.cleanup
		if err := c.cmd.Start(); err != nil {
			c.cleanupNow()
			return nil, fmt.Errorf("沙盒启动失败：%v；直接启动失败：%w", firstErr, err)
		}
		return c.cmd, nil
	}
}

func copyCommandIO(target *exec.Cmd, source *exec.Cmd) {
	target.Stdin = source.Stdin
	target.Stdout = source.Stdout
	target.Stderr = source.Stderr
	target.ExtraFiles = source.ExtraFiles
}

func (directCoreLauncher) Command(launch *launchSession) (*coreCommand, error) {
	cmd := exec.Command(launch.executablePath, launch.args...)
	cmd.Env = launch.env
	cmd.Dir = launch.workingDir
	configureCommand(cmd)
	return newCoreCommand(cmd, nil), nil
}
