//go:build linux

package core

import (
	"log"
	"os"
	"strings"
)

const disableLinuxSandboxEnv = "SPARKLE_CORE_DISABLE_LINUX_SANDBOX"

func newCoreLauncher(launch *launchSession) coreLauncher {
	if sandboxDisabled() {
		log.Printf("核心运行模式：直接启动（%s 已启用）", disableLinuxSandboxEnv)
		return linuxDirectLauncher{}
	}

	mode := CoreRunModeAuto
	if launch != nil && launch.profile.Mode != "" {
		mode = launch.profile.Mode
	}
	switch mode {
	case CoreRunModeDirect:
		log.Printf("核心运行模式：直接启动")
		return linuxDirectLauncher{}
	case CoreRunModeSandbox:
		log.Printf("核心运行模式：强制 chroot 沙盒")
		return linuxSandboxLauncher{}
	case CoreRunModeAuto:
		return linuxAutoLauncher{}
	default:
		log.Printf("核心运行模式 %q 无效，按 auto 处理", mode)
		return linuxAutoLauncher{}
	}
}

func sandboxDisabled() bool {
	value := strings.TrimSpace(os.Getenv(disableLinuxSandboxEnv))
	return value == "1" || strings.EqualFold(value, "true") || strings.EqualFold(value, "yes")
}
