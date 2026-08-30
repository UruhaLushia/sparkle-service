//go:build linux

package core

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/UruhaLushia/sparkle-service/core/sandbox"
)

type linuxSandboxLauncher struct{}

func (linuxSandboxLauncher) Command(launch *launchSession) (*coreCommand, error) {
	serviceExecutable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("读取 service 可执行文件路径失败：%w", err)
	}
	writableDirs := writableDirsFromCoreArgs(launch.args)
	if launch.hookUpFile != "" {
		writableDirs = append(writableDirs, filepath.Dir(launch.hookUpFile))
	}

	cmd, cleanup, err := sandbox.Command(sandbox.Config{
		ExecutablePath: launch.executablePath,
		Args:           launch.args,
		Env:            launch.env,
		WorkingDir:     launch.workingDir,
		ReadOnlyPaths:  []string{serviceExecutable},
		WritablePaths:  launch.profile.SafePaths,
		WritableDirs:   writableDirs,
	})
	if err != nil {
		return nil, err
	}
	return newCoreCommand(cmd, func() {
		if err := cleanup(); err != nil {
			log.Printf("清理核心沙盒失败：%v", err)
		}
	}), nil
}

func writableDirsFromCoreArgs(args []string) []string {
	var dirs []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		value := ""
		switch arg {
		case "-d", "-ext-ctl-unix":
			if i+1 < len(args) {
				value = args[i+1]
				i++
			}
		default:
			if after, ok := strings.CutPrefix(arg, "-d="); ok {
				value = after
			} else if after, ok := strings.CutPrefix(arg, "-ext-ctl-unix="); ok {
				value = after
			}
		}

		if value == "" {
			continue
		}
		if arg == "-ext-ctl-unix" || strings.HasPrefix(arg, "-ext-ctl-unix=") {
			value = filepath.Dir(value)
		}
		dirs = append(dirs, value)
	}
	return dirs
}
