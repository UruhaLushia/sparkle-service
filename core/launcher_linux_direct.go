//go:build linux

package core

import (
	"syscall"
)

type linuxDirectLauncher struct{}

func (linuxDirectLauncher) Command(launch *launchSession) (*coreCommand, error) {
	command, err := (directCoreLauncher{}).Command(launch)
	if err != nil {
		return nil, err
	}
	cmd := command.cmd
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
	return command, nil
}
