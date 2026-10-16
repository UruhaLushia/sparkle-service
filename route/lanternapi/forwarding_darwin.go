package lanternapi

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"time"

	"golang.org/x/sys/unix"
)

type forwardingSetting struct {
	key           string
	before, value uint32
}

type forwardingState struct {
	settings []forwardingSetting
}

func setForwarding(key string, value uint32) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "/usr/sbin/sysctl", "-w", fmt.Sprintf("%s=%d", key, value)).CombinedOutput()
	if err != nil {
		return fmt.Errorf("设置 %s：%w: %s", key, err, output)
	}
	return nil
}

func prepareForwarding(iface string, ipv6 bool) (*forwardingState, error) {
	if _, err := net.InterfaceByName(iface); err != nil {
		return nil, err
	}
	settings := []forwardingSetting{
		{key: "net.inet.ip.forwarding", value: 1},
		{key: "net.inet.ip.redirect", value: 0},
	}
	if ipv6 {
		settings = append(settings,
			forwardingSetting{key: "net.inet6.ip6.forwarding", value: 1},
			forwardingSetting{key: "net.inet6.ip6.redirect", value: 0})
	}
	for i := range settings {
		value, err := unix.SysctlUint32(settings[i].key)
		if err != nil {
			return nil, err
		}
		settings[i].before = value
	}
	f := &forwardingState{}
	for _, setting := range settings {
		if setting.before == setting.value {
			continue
		}
		f.settings = append(f.settings, setting)
		if err := setForwarding(setting.key, setting.value); err != nil {
			return f, errors.Join(err, f.restore())
		}
		value, err := unix.SysctlUint32(setting.key)
		if err == nil && value != setting.value {
			err = fmt.Errorf("%s 未应用预期值 %d", setting.key, setting.value)
		}
		if err != nil {
			return f, errors.Join(err, f.restore())
		}
	}
	return f, nil
}

func (f *forwardingState) restore() error {
	var errs []error
	var pending []forwardingSetting
	for i := len(f.settings) - 1; i >= 0; i-- {
		setting := f.settings[i]
		value, err := unix.SysctlUint32(setting.key)
		if err == nil && value == setting.value {
			err = setForwarding(setting.key, setting.before)
		}
		if err != nil {
			errs = append(errs, err)
			pending = append([]forwardingSetting{setting}, pending...)
		}
	}
	f.settings = pending
	return errors.Join(errs...)
}
