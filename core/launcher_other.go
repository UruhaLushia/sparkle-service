//go:build !linux

package core

func newCoreLauncher(_ *launchSession) coreLauncher {
	return directCoreLauncher{}
}
