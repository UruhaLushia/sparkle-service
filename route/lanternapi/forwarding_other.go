//go:build !linux && !darwin

package lanternapi

import "errors"

type forwardingState struct{}

func prepareForwarding(string, bool) (*forwardingState, error) {
	return nil, errors.New("局域网网关仅支持 Linux 和 macOS")
}

func (*forwardingState) restore() error { return nil }
