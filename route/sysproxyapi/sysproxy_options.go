package sysproxyapi

import "github.com/UruhaLushia/sysproxy-go/sysproxy"

func cloneSysproxyOptions(opt *sysproxy.Options) *sysproxy.Options {
	if opt == nil {
		return &sysproxy.Options{}
	}
	return new(*opt)
}
