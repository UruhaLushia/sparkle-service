//go:build linux

package core

import (
	"syscall"

	"github.com/UruhaLushia/sparkle-service/core/sandbox"
)

type linuxDirectLauncher struct{}

func (linuxDirectLauncher) Command(launch *launchSession) (*coreCommand, error) {
	if len(launch.profile.CPUAffinity) == 0 {
		command, err := (directCoreLauncher{}).Command(launch)
		if err != nil {
			return nil, err
		}
		command.cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
		return command, nil
	}
	command, err := sandbox.NewCommand(sandbox.Config{
		ExecutablePath: launch.executablePath,
		Args:           launch.args,
		Env:            launch.env,
		WorkingDir:     launch.workingDir,
		CPUAffinity:    launch.profile.CPUAffinity,
	})
	if err != nil {
		return nil, err
	}
	return linuxReexecCoreCommand(command), nil
}
