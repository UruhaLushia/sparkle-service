//go:build linux

package core

import (
	"log"

	"github.com/UruhaLushia/sparkle-service/core/sandbox"
)

type linuxAutoLauncher struct{}

func (linuxAutoLauncher) Command(launch *launchSession) (*coreCommand, error) {
	if err := sandbox.Probe(); err != nil {
		log.Printf("核心运行模式：自动回退到直接启动（chroot 沙盒不可用：%v）", err)
		return (linuxDirectLauncher{}).Command(launch)
	}
	log.Printf("核心运行模式：自动选择 chroot 沙盒")
	command, err := (linuxSandboxLauncher{}).Command(launch)
	if err != nil {
		if sandbox.IsConfigError(err) {
			return nil, err
		}
		log.Printf("核心运行模式：自动回退到直接启动（准备 chroot 沙盒失败：%v）", err)
		return (linuxDirectLauncher{}).Command(launch)
	}
	command.startFallback = func(err error) (*coreCommand, error) {
		log.Printf("核心运行模式：自动回退到直接启动（沙盒内启动核心失败：%v）", err)
		return (linuxDirectLauncher{}).Command(launch)
	}
	return command, nil
}
