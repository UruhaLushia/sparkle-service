package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"

	"github.com/UruhaLushia/sparkle-service/core/controller"
	"github.com/UruhaLushia/sparkle-service/core/security"
)

type LaunchProfile struct {
	CorePath         string            `json:"core_path,omitempty"`
	Mode             string            `json:"mode,omitempty"`
	Args             []string          `json:"args,omitempty"`
	SafePaths        []string          `json:"safe_paths,omitempty"`
	Env              map[string]string `json:"env,omitempty"`
	Priority         string            `json:"mihomo_cpu_priority,omitempty"`
	LogPath          string            `json:"log_path,omitempty"`
	SaveLogs         *bool             `json:"save_logs,omitempty"`
	MaxLogFileSizeMB int               `json:"max_log_file_size_mb,omitempty"`
}

type LaunchProfilePatch struct {
	Mode             *string `json:"mode,omitempty"`
	LogPath          *string `json:"log_path,omitempty"`
	SaveLogs         *bool   `json:"save_logs,omitempty"`
	MaxLogFileSizeMB *int    `json:"max_log_file_size_mb,omitempty"`
}

const (
	CoreRunModeAuto    = "auto"
	CoreRunModeSandbox = "sandbox"
	CoreRunModeDirect  = "direct"
)

type launchSession struct {
	sourcePath     string
	executablePath string
	workingDir     string
	args           []string
	env            []string
	hookUpFile     string
	waitReady      func(context.Context) error
	readyNotify    <-chan struct{}
	cpuPriority    string
	logPath        string
	saveLogs       bool
	maxLogBytes    int64
	logWriter      *boundedLogWriter
	fileAccess     fileAccess
	controllerNet  string
	controllerAddr string
	profile        LaunchProfile
	cleanup        func()
}

func (s *launchSession) cleanupNow() {
	if s == nil || s.cleanup == nil {
		return
	}
	s.cleanup()
	s.cleanup = nil
}

func LoadLaunchProfile() (LaunchProfile, error) {
	path := launchProfilePath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return LaunchProfile{Mode: CoreRunModeAuto}, nil
		}
		return LaunchProfile{}, fmt.Errorf("读取核心启动配置失败：%w", err)
	}

	var profile LaunchProfile
	if err := json.Unmarshal(data, &profile); err != nil {
		return LaunchProfile{}, fmt.Errorf("解析核心启动配置失败：%w", err)
	}

	return normalizeLaunchProfile(profile)
}

func SaveLaunchProfile(profile LaunchProfile) error {
	normalized, err := normalizeLaunchProfile(profile)
	if err != nil {
		return err
	}

	path := launchProfilePath()
	if isZeroLaunchProfile(normalized) {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("清理核心启动配置失败：%w", err)
		}
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建核心配置目录失败：%w", err)
	}

	data, err := json.MarshalIndent(normalized, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化核心启动配置失败：%w", err)
	}

	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("保存核心启动配置失败：%w", err)
	}

	return nil
}

func PatchLaunchProfile(patch LaunchProfilePatch) (LaunchProfile, error) {
	profile, err := LoadLaunchProfile()
	if err != nil {
		return LaunchProfile{}, err
	}

	if patch.Mode != nil {
		profile.Mode = *patch.Mode
	}
	if patch.LogPath != nil {
		profile.LogPath = *patch.LogPath
	}
	if patch.SaveLogs != nil {
		profile.SaveLogs = new(*patch.SaveLogs)
	}
	if patch.MaxLogFileSizeMB != nil {
		profile.MaxLogFileSizeMB = *patch.MaxLogFileSizeMB
	}

	if err := SaveLaunchProfile(profile); err != nil {
		return LaunchProfile{}, err
	}

	return LoadLaunchProfile()
}

func (cm *CoreManager) prepareLaunchSession(profileOverride *LaunchProfile, options launchOptions) (*launchSession, error) {
	profile, err := resolveLaunchProfile(profileOverride)
	if err != nil {
		return nil, err
	}

	corePath, err := resolveCoreExecutablePath(profile.CorePath, true)
	if err != nil {
		return nil, err
	}
	profile.CorePath = corePath
	saveLogs := true
	if profile.SaveLogs != nil {
		saveLogs = *profile.SaveLogs
	}

	if err := security.SecureBinary(corePath); err != nil {
		return nil, err
	}

	hook, err := createCoreStartupHook()
	if err != nil {
		return nil, err
	}

	args, controllerNet, controllerAddr, controllerCleanup, err := configureManagedController(profile.Args)
	if err != nil {
		hook.cleanup()
		return nil, err
	}
	args = append([]string{"-post-up", hook.postUpCommand, "-post-down", hook.postDownCommand}, args...)

	workingDir, err := resolveLaunchWorkingDir(corePath, args)
	if err != nil {
		hook.cleanup()
		if controllerCleanup != nil {
			controllerCleanup()
		}
		return nil, err
	}

	return &launchSession{
		sourcePath:     corePath,
		executablePath: corePath,
		workingDir:     workingDir,
		args:           args,
		env:            buildLaunchEnv(profile, hook.env),
		hookUpFile:     hook.upFile,
		waitReady:      hook.wait,
		readyNotify:    hook.notifications,
		cpuPriority:    profile.Priority,
		logPath:        profile.LogPath,
		saveLogs:       saveLogs,
		maxLogBytes:    maxLogFileSizeBytes(profile.MaxLogFileSizeMB),
		fileAccess:     options.fileAccess,
		controllerNet:  controllerNet,
		controllerAddr: controllerAddr,
		profile:        profile,
		cleanup: func() {
			if controllerCleanup != nil {
				controllerCleanup()
			}
			hook.cleanup()
		},
	}, nil
}

func resolveLaunchProfile(profileOverride *LaunchProfile) (LaunchProfile, error) {
	if profileOverride != nil {
		return normalizeLaunchProfile(*profileOverride)
	}
	return LoadLaunchProfile()
}

func normalizeLaunchProfile(profile LaunchProfile) (LaunchProfile, error) {
	normalized := LaunchProfile{
		CorePath:         strings.TrimSpace(profile.CorePath),
		Mode:             strings.ToLower(strings.TrimSpace(profile.Mode)),
		Priority:         strings.TrimSpace(profile.Priority),
		LogPath:          strings.TrimSpace(profile.LogPath),
		MaxLogFileSizeMB: profile.MaxLogFileSizeMB,
	}
	if normalized.Mode == "" {
		normalized.Mode = CoreRunModeAuto
	}
	switch normalized.Mode {
	case CoreRunModeAuto, CoreRunModeSandbox, CoreRunModeDirect:
	default:
		return LaunchProfile{}, fmt.Errorf("mode 仅支持 auto、sandbox 或 direct")
	}
	if profile.SaveLogs != nil {
		normalized.SaveLogs = new(*profile.SaveLogs)
	}

	if len(profile.Args) > 0 {
		normalized.Args = append(normalized.Args, profile.Args...)
		if err := validateCoreArgs(normalized.Args); err != nil {
			return LaunchProfile{}, err
		}
	}

	if len(profile.SafePaths) > 0 {
		normalized.SafePaths = make([]string, 0, len(profile.SafePaths))
		for _, path := range profile.SafePaths {
			trimmedPath := strings.TrimSpace(path)
			if trimmedPath == "" {
				continue
			}
			absPath, err := filepath.Abs(trimmedPath)
			if err != nil {
				return LaunchProfile{}, fmt.Errorf("解析可信路径失败 %q：%w", trimmedPath, err)
			}
			normalized.SafePaths = append(normalized.SafePaths, absPath)
		}
	}

	if len(profile.Env) > 0 {
		normalized.Env = make(map[string]string, len(profile.Env))
		for key, value := range profile.Env {
			key = strings.TrimSpace(key)
			if key == "" {
				return LaunchProfile{}, fmt.Errorf("环境变量名不能为空")
			}
			normalized.Env[key] = value
		}
	}

	if normalized.LogPath != "" {
		absPath, err := filepath.Abs(normalized.LogPath)
		if err != nil {
			return LaunchProfile{}, fmt.Errorf("解析核心日志路径失败 %q：%w", normalized.LogPath, err)
		}
		normalized.LogPath = absPath
	}

	if isZeroLaunchProfile(normalized) {
		return normalized, nil
	}

	if normalized.CorePath == "" {
		return LaunchProfile{}, fmt.Errorf("core_path 不能为空")
	}

	corePath, err := resolveCoreExecutablePath(normalized.CorePath, false)
	if err != nil {
		return LaunchProfile{}, err
	}
	normalized.CorePath = corePath

	return normalized, nil
}

func validateCoreArgs(args []string) error {
	for i, arg := range args {
		if arg == "" {
			return fmt.Errorf("启动参数第 %d 项为空", i)
		}
		if arg == "--" {
			return fmt.Errorf("-- 会截断 service 管理的启动参数，不能由客户端传入")
		}
		name, ok := coreArgName(arg)
		if !ok {
			continue
		}
		switch name {
		case "post-up", "post-down":
			return fmt.Errorf("-%s 由 service 管理，不能由客户端传入", name)
		case "t", "v":
			return fmt.Errorf("-%s 不是运行态启动参数", name)
		}
	}
	return nil
}

func resolveLaunchWorkingDir(corePath string, args []string) (string, error) {
	workingDir := filepath.Dir(corePath)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "-d":
			if i+1 < len(args) {
				workingDir = args[i+1]
				i++
			}
		default:
			if after, ok := strings.CutPrefix(arg, "-d="); ok {
				workingDir = after
			}
		}
	}

	workingDir = strings.TrimSpace(workingDir)
	if workingDir == "" {
		return "", fmt.Errorf("核心工作目录不能为空")
	}
	if !filepath.IsAbs(workingDir) {
		absPath, err := filepath.Abs(workingDir)
		if err != nil {
			return "", fmt.Errorf("解析核心工作目录失败 %q：%w", workingDir, err)
		}
		workingDir = absPath
	}
	return filepath.Clean(workingDir), nil
}

func configureManagedController(args []string) ([]string, string, string, func(), error) {
	controllerNet, controllerAddr, cleanup, err := controller.CreatePrivateEndpoint()
	if err != nil {
		return nil, "", "", nil, err
	}

	filteredArgs := stripControllerArgs(args)
	switch controllerNet {
	case "pipe":
		filteredArgs = append([]string{"-ext-ctl-pipe", controllerAddr}, filteredArgs...)
	case "unix":
		filteredArgs = append([]string{"-ext-ctl-unix", controllerAddr}, filteredArgs...)
	default:
		if cleanup != nil {
			cleanup()
		}
		return nil, "", "", nil, fmt.Errorf("不支持的核心控制器网络: %s", controllerNet)
	}

	return filteredArgs, controllerNet, controllerAddr, cleanup, nil
}

func stripControllerArgs(args []string) []string {
	filtered := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if isControllerArg(arg) {
			if !strings.Contains(arg, "=") && i+1 < len(args) {
				i++
			}
			continue
		}
		filtered = append(filtered, arg)
	}
	return filtered
}

func isControllerArg(arg string) bool {
	name, ok := coreArgName(arg)
	if !ok {
		return false
	}
	return name == "ext-ctl" || name == "ext-ctl-pipe" || name == "ext-ctl-unix"
}

func coreArgName(arg string) (string, bool) {
	if !strings.HasPrefix(arg, "-") || arg == "-" {
		return "", false
	}
	trimmed := strings.TrimLeft(arg, "-")
	if trimmed == "" {
		return "", false
	}
	name, _, _ := strings.Cut(trimmed, "=")
	return name, name != ""
}

func isZeroLaunchProfile(profile LaunchProfile) bool {
	return profile.CorePath == "" &&
		(profile.Mode == "" || profile.Mode == CoreRunModeAuto) &&
		profile.Priority == "" &&
		profile.LogPath == "" &&
		profile.SaveLogs == nil &&
		profile.MaxLogFileSizeMB == 0 &&
		len(profile.Args) == 0 &&
		len(profile.SafePaths) == 0 &&
		len(profile.Env) == 0
}

func maxLogFileSizeBytes(mb int) int64 {
	if mb <= 0 {
		mb = 20
	}
	return int64(mb) * 1024 * 1024
}

func resolveCoreExecutablePath(rawPath string, allowEnv bool) (string, error) {
	path := strings.TrimSpace(rawPath)
	if path == "" && allowEnv {
		path = strings.TrimSpace(os.Getenv("SPARKLE_CORE_PATH"))
	}
	if path == "" {
		return "", fmt.Errorf("未配置核心路径")
	}

	if !filepath.IsAbs(path) {
		absPath, err := filepath.Abs(path)
		if err != nil {
			return "", fmt.Errorf("解析核心路径失败：%w", err)
		}
		path = absPath
	}

	candidates := []string{path}
	if runtime.GOOS == "windows" && filepath.Ext(path) == "" {
		candidates = append([]string{path + ".exe"}, candidates...)
	}

	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err == nil {
			if info.IsDir() {
				return "", fmt.Errorf("核心路径指向目录而非可执行文件: %s", candidate)
			}
			return candidate, nil
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("检查核心路径失败：%w", err)
		}
	}

	return "", fmt.Errorf("核心可执行文件不存在: %s", path)
}

type coreStartupHook struct {
	upFile          string
	postUpCommand   string
	postDownCommand string
	env             map[string]string
	wait            func(context.Context) error
	notifications   <-chan struct{}
	cleanup         func()
}

func createCoreStartupHook() (*coreStartupHook, error) {
	token, err := randomToken(16)
	if err != nil {
		return nil, err
	}

	return createNativeStartupHook(token)
}

func newCoreStartupHook(waitNotification func() (bool, error), upFile string, postUpCommand string, postDownCommand string, env map[string]string, cleanup func()) *coreStartupHook {
	firstReady := make(chan error, 1)
	notifications := make(chan struct{}, 8)
	var firstDelivered atomic.Bool

	deliverFirst := func(err error) bool {
		if !firstDelivered.CompareAndSwap(false, true) {
			return false
		}
		firstReady <- err
		return true
	}

	go func() {
		for {
			fatal, err := waitNotification()
			if err != nil {
				deliverFirst(err)
				if fatal {
					return
				}
				continue
			}
			if deliverFirst(nil) {
				continue
			}

			select {
			case notifications <- struct{}{}:
			default:
			}
		}
	}()

	return &coreStartupHook{
		upFile:          upFile,
		postUpCommand:   postUpCommand,
		postDownCommand: postDownCommand,
		env:             env,
		wait: func(ctx context.Context) error {
			select {
			case err := <-firstReady:
				return err
			case <-ctx.Done():
				return ctx.Err()
			}
		},
		notifications: notifications,
		cleanup:       cleanup,
	}
}

func randomToken(size int) (string, error) {
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("生成启动 hook token 失败：%w", err)
	}
	return hex.EncodeToString(data), nil
}

func noopShellCommand() string {
	if runtime.GOOS == "windows" {
		return "exit /b 0"
	}
	return "true"
}

func buildLaunchEnv(profile LaunchProfile, managed map[string]string) []string {
	envMap := make(map[string]string)

	maps.Copy(envMap, profile.Env)
	ensureEssentialEnv(envMap)

	envMap["SAFE_PATHS"] = strings.Join(profile.SafePaths, string(os.PathListSeparator))
	envMap["SKIP_SAFE_PATH_CHECK"] = "false"
	envMap["CLASH_CONFIG_STRING"] = ""
	envMap["CLASH_HOME_DIR"] = ""
	envMap["CLASH_CONFIG_FILE"] = ""
	envMap["CLASH_OVERRIDE_EXTERNAL_UI_DIR"] = ""
	envMap["CLASH_OVERRIDE_EXTERNAL_CONTROLLER"] = ""
	envMap["CLASH_OVERRIDE_EXTERNAL_CONTROLLER_UNIX"] = ""
	envMap["CLASH_OVERRIDE_EXTERNAL_CONTROLLER_PIPE"] = ""
	envMap["CLASH_OVERRIDE_SECRET"] = ""
	envMap["CLASH_POST_UP"] = ""
	envMap["CLASH_POST_DOWN"] = ""
	maps.Copy(envMap, managed)

	env := make([]string, 0, len(envMap))
	for key, value := range envMap {
		env = append(env, key+"="+value)
	}
	return env
}

func ensureEssentialEnv(envMap map[string]string) {
	copyIfMissing := func(key string) {
		if _, ok := lookupEnvMap(envMap, key); ok {
			return
		}
		if value, ok := os.LookupEnv(key); ok {
			envMap[key] = value
		}
	}

	if runtime.GOOS == "windows" {
		for _, key := range []string{
			"SystemRoot",
			"WINDIR",
			"ComSpec",
			"PATHEXT",
			"ProgramData",
			"ProgramFiles",
			"ProgramFiles(x86)",
			"CommonProgramFiles",
			"OS",
			"PROCESSOR_ARCHITECTURE",
			"NUMBER_OF_PROCESSORS",
			"PATH",
			"Path",
		} {
			copyIfMissing(key)
		}
		return
	}

	copyIfMissing("PATH")
}

func lookupEnvMap(envMap map[string]string, key string) (string, bool) {
	if value, ok := envMap[key]; ok {
		return value, true
	}
	if runtime.GOOS != "windows" {
		return "", false
	}
	for itemKey, value := range envMap {
		if strings.EqualFold(itemKey, key) {
			return value, true
		}
	}
	return "", false
}

func launchProfilePath() string {
	return filepath.Join(serviceConfigDir(), "sparkle", "core", "launch_profile.json")
}

func serviceConfigDir() string {
	if dir := os.Getenv("SPARKLE_CONFIG_DIR"); dir != "" {
		return dir
	}

	switch runtime.GOOS {
	case "windows":
		return `C:\ProgramData`
	case "darwin":
		return filepath.Join("/var/root", "Library", "Application Support")
	default:
		return filepath.Join("/root", ".config")
	}
}
