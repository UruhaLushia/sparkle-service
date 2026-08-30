package cmd

import (
	"errors"
	"runtime"

	"github.com/spf13/cobra"
)

var (
	server string
	bypass string
	pacUrl string

	device           string
	onlyActiveDevice bool
	useRegistry      bool

	listen      string
	defaultAddr string
)

var MainCmd = &cobra.Command{
	Use:           "sparkle-service",
	Short:         "Sparkle Service",
	SilenceErrors: true,
	SilenceUsage:  true,
}

type reportedError struct {
	err error
}

func (e reportedError) Error() string {
	return e.err.Error()
}

func (e reportedError) Unwrap() error {
	return e.err
}

func newReportedError(err error) error {
	return reportedError{err: err}
}

func IsReportedError(err error) bool {
	_, ok := errors.AsType[reportedError](err)
	return ok
}

func init() {
	if runtime.GOOS == "windows" {
		defaultAddr = "\\\\.\\pipe\\sparkle\\service"
	} else {
		defaultAddr = "/tmp/sparkle-service.sock"
	}

	MainCmd.AddCommand(sysproxyCmd)
	MainCmd.AddCommand(serverCmd)
	MainCmd.AddCommand(serviceCmd)

	sysproxyCmd.AddCommand(proxyCmd)
	sysproxyCmd.AddCommand(pacCmd)
	sysproxyCmd.AddCommand(disableCmd)
	sysproxyCmd.AddCommand(statusCmd)

	MainCmd.PersistentFlags().BoolVarP(&onlyActiveDevice, "only-active-device", "a", false, "仅对活跃的网络设备生效")
	MainCmd.PersistentFlags().BoolVarP(&useRegistry, "use-registry", "r", false, "使用注册表设置")
	MainCmd.PersistentFlags().StringVarP(&device, "device", "d", "", "指定网络设备")
	MainCmd.PersistentFlags().StringVarP(&listen, "listen", "l", defaultAddr, "监听地址")

	proxyCmd.Flags().StringVarP(&server, "server", "s", "", "代理服务器地址")
	proxyCmd.Flags().StringVarP(&bypass, "bypass", "b", "", "绕过地址")

	pacCmd.Flags().StringVarP(&pacUrl, "url", "u", "", "pac 地址")
}
