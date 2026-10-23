package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/UruhaLushia/sparkle-service/log"
	"github.com/UruhaLushia/sparkle-service/route"
	appservice "github.com/UruhaLushia/sparkle-service/service"

	kservice "github.com/kardianos/service"
	"github.com/spf13/cobra"
)

type Program struct {
	listen string
}

type serviceCommandStatus struct {
	Action  string `json:"action,omitempty"`
	State   string `json:"state,omitempty"`
	Success bool   `json:"success"`
	Changed bool   `json:"changed,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (p *Program) Start(s kservice.Service) error {
	go p.run()
	return nil
}

func (p *Program) run() {
	logFile, err := log.InitLogging()
	if err != nil {
		log.Printf("初始化日志失败：%v\n", err)
	}
	if logFile != nil {
		defer logFile.Close()
	}
	log.Println("服务启动中...")

	if err := route.Start(p.listen); err != nil {
		log.Fatal(err)
	}
}

func (p *Program) Stop(s kservice.Service) error {
	log.Println("服务停止中...")
	if err := route.Stop(); err != nil {
		log.Printf("服务停止清理失败：%v", err)
		return err
	}
	log.Println("服务已停止")
	return nil
}

func outputServiceCommandResult(message string, status serviceCommandStatus) error {
	status.Success = true
	log.S().Infow(message, "status", status)
	return nil
}

func outputServiceCommandError(action, message string, err error) error {
	if err == nil {
		return nil
	}
	log.S().Errorw(message, "status", serviceCommandStatus{
		Action:  action,
		State:   serviceErrorState(err),
		Success: false,
		Error:   err.Error(),
	})
	return newReportedError(err)
}

func serviceErrorState(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, kservice.ErrNotInstalled) || strings.Contains(strings.ToLower(err.Error()), "service is not installed") {
		return "not-installed"
	}
	return ""
}

func normalizeServiceStatus(status kservice.Status) string {
	switch status {
	case kservice.StatusRunning:
		return "running"
	case kservice.StatusStopped:
		return "stopped"
	case kservice.StatusUnknown:
		return "unknown"
	default:
		return "unknown"
	}
}

func serviceStatusMessage(state string) string {
	switch state {
	case "running":
		return "服务状态：运行中"
	case "stopped":
		return "服务状态：已停止"
	default:
		return "服务状态：未知"
	}
}

var serviceInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "安装 Sparkle 服务",
	RunE: func(cmd *cobra.Command, args []string) error {
		listenAddr := listen
		if listenAddr == "" {
			listenAddr = defaultAddr
		}

		prg := &Program{listen: listenAddr}
		s, err := appservice.New(prg, os.Args[0])
		if err != nil {
			return outputServiceCommandError("install", "创建服务失败", err)
		}

		if err := s.Install(); err != nil {
			return outputServiceCommandError("install", "安装服务失败", err)
		}
		if err := s.Start(); err != nil {
			return outputServiceCommandError("install", "启动服务失败", err)
		}
		return outputServiceCommandResult("服务安装成功", serviceCommandStatus{Action: "install", State: "running"})
	},
}

var serviceUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "卸载 Sparkle 服务",
	RunE: func(cmd *cobra.Command, args []string) error {
		listenAddr := listen
		if listenAddr == "" {
			listenAddr = defaultAddr
		}
		prg := &Program{listen: listenAddr}
		s, err := appservice.New(prg, "")
		if err != nil {
			return outputServiceCommandError("uninstall", "创建服务失败", err)
		}

		if err := s.Stop(); err != nil {
			return outputServiceCommandError("uninstall", "停止服务失败", err)
		}
		if err := s.Uninstall(); err != nil {
			return outputServiceCommandError("uninstall", "卸载服务失败", err)
		}
		if err := cleanupServiceRuntimeExecutable(); err != nil {
			return outputServiceCommandError("uninstall", "清理服务运行副本失败", err)
		}
		return outputServiceCommandResult("服务卸载成功", serviceCommandStatus{Action: "uninstall", State: "not-installed"})
	},
}

var serviceStartCmd = &cobra.Command{
	Use:   "start",
	Short: "启动 Sparkle 服务",
	RunE: func(cmd *cobra.Command, args []string) error {
		listenAddr := listen
		if listenAddr == "" {
			listenAddr = defaultAddr
		}
		prg := &Program{listen: listenAddr}
		s, err := appservice.New(prg, "")
		if err != nil {
			return outputServiceCommandError("start", "创建服务失败", err)
		}

		if err := s.Start(); err != nil {
			return outputServiceCommandError("start", "启动服务失败", err)
		}
		return outputServiceCommandResult("服务启动成功", serviceCommandStatus{Action: "start", State: "running"})
	},
}

var serviceStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "停止 Sparkle 服务",
	RunE: func(cmd *cobra.Command, args []string) error {
		listenAddr := listen
		if listenAddr == "" {
			listenAddr = defaultAddr
		}
		prg := &Program{listen: listenAddr}
		s, err := appservice.New(prg, "")
		if err != nil {
			return outputServiceCommandError("stop", "创建服务失败", err)
		}

		if err := s.Stop(); err != nil {
			return outputServiceCommandError("stop", "停止服务失败", err)
		}
		return outputServiceCommandResult("服务停止成功", serviceCommandStatus{Action: "stop", State: "stopped"})
	},
}

var serviceRestartCmd = &cobra.Command{
	Use:   "restart",
	Short: "重启 Sparkle 服务",
	RunE: func(cmd *cobra.Command, args []string) error {
		listenAddr := listen
		if listenAddr == "" {
			listenAddr = defaultAddr
		}
		prg := &Program{listen: listenAddr}
		s, err := appservice.New(prg, "")
		if err != nil {
			return outputServiceCommandError("restart", "创建服务失败", err)
		}

		if err := s.Restart(); err != nil {
			return outputServiceCommandError("restart", "重启服务失败", err)
		}
		return outputServiceCommandResult("服务重启成功", serviceCommandStatus{Action: "restart", State: "running"})
	},
}

var serviceStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "查看 Sparkle 服务状态",
	RunE: func(cmd *cobra.Command, args []string) error {
		listenAddr := listen
		if listenAddr == "" {
			listenAddr = defaultAddr
		}
		prg := &Program{listen: listenAddr}
		s, err := appservice.New(prg, "")
		if err != nil {
			return outputServiceCommandError("status", "创建服务失败", err)
		}

		status, err := s.Status()
		if err != nil {
			return outputServiceCommandError("status", "查询服务状态失败", err)
		}

		state := normalizeServiceStatus(status)
		return outputServiceCommandResult(serviceStatusMessage(state), serviceCommandStatus{Action: "status", State: state})
	},
}

var serviceRunCmd = &cobra.Command{
	Use:   "run",
	Short: "运行 Sparkle 服务",
	Run: func(cmd *cobra.Command, args []string) {
		if err := ensureServiceRuntimeExecutable(); err != nil {
			log.Fatal(err)
		}

		listenAddr := listen
		if listenAddr == "" {
			listenAddr = defaultAddr
		}
		prg := &Program{listen: listenAddr}
		s, err := appservice.New(prg, "")
		if err != nil {
			log.Fatal(err)
		}

		if err := s.Run(); err != nil {
			log.Fatal(err)
		}
	},
}

var serviceCmd = &cobra.Command{
	Use:   "service",
	Short: "管理 Sparkle 服务",
}

var serviceInitCmd = &cobra.Command{
	Use:   "init",
	Short: "初始化服务（传入公钥）",
	RunE: func(cmd *cobra.Command, args []string) error {
		publicKey := cmd.Flag("public-key").Value.String()
		authorizedSID := cmd.Flag("authorized-sid").Value.String()
		authorizedUID, _ := cmd.Flags().GetUint32("authorized-uid")
		if publicKey == "" {
			return outputServiceCommandError("init", "错误：必须通过 --public-key 参数提供公钥", errors.New("必须通过 --public-key 参数提供公钥"))
		}
		if authorizedSID == "" && !cmd.Flags().Changed("authorized-uid") {
			return outputServiceCommandError("init", "错误：必须通过 --authorized-sid 或 --authorized-uid 绑定允许访问服务的用户身份", errors.New("必须通过 --authorized-sid 或 --authorized-uid 绑定允许访问服务的用户身份"))
		}
		userDataDir := route.GetConfigDir()
		keyDir := filepath.Join(userDataDir, "sparkle", "keys")

		_ = route.InitKeyManager(keyDir)

		km := route.GetKeyManager()
		keyChanged, err := km.SetPublicKey(publicKey)
		if err != nil {
			return outputServiceCommandError("init", "设置公钥失败", err)
		}

		principalChanged := false
		switch {
		case authorizedSID != "":
			principalChanged, err = km.SetAuthorizedSID(authorizedSID)
			if err != nil {
				return outputServiceCommandError("init", "设置授权 SID 失败", err)
			}
		case cmd.Flags().Changed("authorized-uid"):
			principalChanged, err = km.SetAuthorizedUID(authorizedUID)
			if err != nil {
				return outputServiceCommandError("init", "设置授权 UID 失败", err)
			}
		}

		changed := keyChanged || principalChanged
		if changed {
			_ = outputServiceCommandResult("服务初始化成功，认证配置已更新", serviceCommandStatus{Action: "init", Changed: true})
		} else {
			_ = outputServiceCommandResult("服务初始化成功，认证配置未变化", serviceCommandStatus{Action: "init"})
		}

		listenAddr := listen
		if listenAddr == "" {
			listenAddr = defaultAddr
		}
		prg := &Program{listen: listenAddr}
		s, err := appservice.New(prg, "")
		if err != nil {
			return outputServiceCommandError("init", "创建服务失败", err)
		}

		status, err := s.Status()
		if err != nil {
			return outputServiceCommandError("status", "查询服务状态失败；如果服务正在运行，请手动执行 'restart' 命令", err)
		}

		state := normalizeServiceStatus(status)
		if status == kservice.StatusRunning {
			if !changed {
				return outputServiceCommandResult("服务已在运行，配置未变化，无需重启", serviceCommandStatus{Action: "init", State: state})
			}
			log.S().Infow("正在重启服务...", "status", serviceCommandStatus{Action: "restart", State: state, Success: true})
			if err := s.Restart(); err != nil {
				return outputServiceCommandError("restart", "重启服务失败；请手动执行 'sparkle-service service restart' 命令", err)
			}
			return outputServiceCommandResult("服务已成功重启", serviceCommandStatus{Action: "restart", State: "running"})
		}

		return outputServiceCommandResult("服务未运行，配置将在下次启动时生效", serviceCommandStatus{Action: "init", State: state, Changed: changed})
	},
}

func init() {
	serviceCmd.AddCommand(serviceInitCmd)
	serviceCmd.AddCommand(serviceInstallCmd)
	serviceCmd.AddCommand(serviceUninstallCmd)
	serviceCmd.AddCommand(serviceStartCmd)
	serviceCmd.AddCommand(serviceStopCmd)
	serviceCmd.AddCommand(serviceRestartCmd)
	serviceCmd.AddCommand(serviceStatusCmd)
	serviceCmd.AddCommand(serviceRunCmd)

	serviceInitCmd.Flags().StringP("public-key", "k", "", "客户端公钥")
	serviceInitCmd.Flags().String("authorized-sid", "", "允许访问服务的 Windows SID")
	serviceInitCmd.Flags().Uint32("authorized-uid", 0, "允许访问服务的 Unix UID")
}
