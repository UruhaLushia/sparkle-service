package lanternapi

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type forwardingSetting struct {
	path, before, value string
}

type forwardingState struct {
	settings []forwardingSetting
}

func prepareForwarding(iface string, ipv6 bool) (*forwardingState, error) {
	if iface == "" || strings.ContainsAny(iface, "/\\") || iface == "." || iface == ".." {
		return nil, errors.New("无效的局域网接口")
	}
	settings := []forwardingSetting{
		{path: "net/ipv4/conf/" + iface + "/forwarding", value: "1"},
		{path: "net/ipv4/conf/" + iface + "/send_redirects", value: "0"},
		{path: "net/ipv4/conf/all/send_redirects", value: "0"},
	}
	if ipv6 {
		settings = append(settings, forwardingSetting{path: "net/ipv6/conf/all/forwarding", value: "1"})
		paths, err := filepath.Glob("/proc/sys/net/ipv6/conf/*/forwarding")
		if err != nil {
			return nil, err
		}
		for _, path := range paths {
			if filepath.Base(filepath.Dir(path)) != "all" {
				settings = append(settings, forwardingSetting{path: path})
			}
		}
		settings = append(settings, forwardingSetting{path: "net/ipv6/conf/" + iface + "/accept_ra", value: "2"})
	}
	f := &forwardingState{}
	for _, setting := range settings {
		if !filepath.IsAbs(setting.path) {
			setting.path = filepath.Join("/proc/sys", setting.path)
		}
		b, err := os.ReadFile(setting.path)
		if err != nil {
			return nil, err
		}
		setting.before = strings.TrimSpace(string(b))
		if setting.value == "" {
			setting.value = setting.before
			if filepath.Base(filepath.Dir(setting.path)) == iface {
				setting.value = "1"
			}
		}
		if setting.before != setting.value || strings.HasPrefix(setting.path, "/proc/sys/net/ipv6/conf/") && strings.HasSuffix(setting.path, "/forwarding") {
			f.settings = append(f.settings, setting)
		}
	}
	for _, setting := range f.settings {
		if err := os.WriteFile(setting.path, []byte(setting.value), 0); err != nil {
			for i := range f.settings {
				if current, readErr := os.ReadFile(f.settings[i].path); readErr == nil {
					f.settings[i].value = strings.TrimSpace(string(current))
				}
			}
			return f, errors.Join(fmt.Errorf("启用接口转发：%w", err), f.restore())
		}
	}
	return f, nil
}

func (f *forwardingState) restore() error {
	var errs []error
	var restore []forwardingSetting
	for _, setting := range f.settings {
		b, err := os.ReadFile(setting.path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err == nil && strings.TrimSpace(string(b)) == setting.value {
			setting.value = setting.before
		} else if err == nil {
			setting.value = strings.TrimSpace(string(b))
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("恢复接口转发：%w", err))
		} else {
			restore = append(restore, setting)
		}
	}
	if len(errs) != 0 {
		return errors.Join(errs...)
	}
	for _, setting := range restore {
		if err := os.WriteFile(setting.path, []byte(setting.value), 0); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("恢复接口转发：%w", err))
		}
	}
	if len(errs) == 0 {
		f.settings = nil
	}
	return errors.Join(errs...)
}
