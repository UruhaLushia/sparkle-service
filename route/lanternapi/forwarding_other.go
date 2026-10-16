//go:build !linux

package lanternapi

import "errors"

type forwardingState struct{}

func prepareForwarding(string, bool) (*forwardingState, error) {
	return nil, errors.New("局域网网关暂仅支持 Linux")
}

func (*forwardingState) restore() error { return nil }
