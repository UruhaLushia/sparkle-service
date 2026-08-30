//go:build linux

package sandbox

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const sandboxRootPrefix = "sparkle-core-sandbox-"

type configError struct {
	err error
}

func (e *configError) Error() string {
	return e.err.Error()
}

func (e *configError) Unwrap() error {
	return e.err
}

func IsConfigError(err error) bool {
	_, ok := errors.AsType[*configError](err)
	return ok
}

type Config struct {
	ExecutablePath string
	Args           []string
	Env            []string
	WorkingDir     string
	ReadOnlyPaths  []string
	WritablePaths  []string
	WritableDirs   []string
}

type sandboxMount struct {
	source   string
	target   string
	readOnly bool
	file     bool
	proc     bool
}

func Command(config Config) (*exec.Cmd, func() error, error) {
	root, cleanup, err := prepareRoot(config)
	if err != nil {
		return nil, nil, err
	}

	cmd := exec.Command(config.ExecutablePath, config.Args...)
	cmd.Env = config.Env
	cmd.Dir = config.WorkingDir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.SysProcAttr.Chroot = root
	cmd.SysProcAttr.Cloneflags |= syscall.CLONE_NEWIPC | syscall.CLONE_NEWUTS
	// Keep the host network namespace: TUN cores must be able to update the host
	// routes, policy rules and netfilter state through netlink/iptables/nftables.
	cmd.SysProcAttr.Unshareflags |= syscall.CLONE_NEWNS
	cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL

	return cmd, cleanup, nil
}

func prepareRoot(config Config) (string, func() error, error) {
	if err := ensureXTablesLock(); err != nil {
		return "", nil, err
	}
	mounts, err := sandboxMounts(config)
	if err != nil {
		return "", nil, err
	}

	cleanupStaleLinuxSandboxRoots()

	rootTemplate, err := sandboxRootTemplate()
	if err != nil {
		return "", nil, err
	}
	root, err := os.MkdirTemp("", rootTemplate)
	if err != nil {
		return "", nil, fmt.Errorf("创建核心沙盒目录失败：%w", err)
	}
	if err := mountSandboxRoot(root); err != nil {
		_ = os.RemoveAll(root)
		return "", nil, err
	}
	if err := prepareLinuxSandboxStaticLayout(root); err != nil {
		_ = cleanupLinuxSandboxRoot(root)
		return "", nil, err
	}

	mounted := make([]string, 0, len(mounts))
	cleanup := func() error {
		var cleanupErr error
		for _, mountPoint := range slices.Backward(mounted) {
			if err := makeSandboxMountPrivate(mountPoint); err != nil {
				if cleanupErr == nil && !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.ENOENT) {
					cleanupErr = fmt.Errorf("隔离沙盒映射失败 %s：%w", mountPoint, err)
				}
				continue
			}
			if err := syscall.Unmount(mountPoint, syscall.MNT_DETACH); err != nil && cleanupErr == nil {
				cleanupErr = fmt.Errorf("卸载沙盒映射失败 %s：%w", mountPoint, err)
			}
		}
		if err := makeSandboxMountPrivate(root); err != nil {
			if cleanupErr == nil && !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.ENOENT) {
				cleanupErr = fmt.Errorf("隔离核心沙盒根目录失败 %s：%w", root, err)
			}
		} else if err := syscall.Unmount(root, syscall.MNT_DETACH); err != nil &&
			cleanupErr == nil &&
			!errors.Is(err, syscall.EINVAL) &&
			!errors.Is(err, syscall.ENOENT) {
			cleanupErr = fmt.Errorf("卸载核心沙盒根目录失败 %s：%w", root, err)
		}
		if err := os.RemoveAll(root); err != nil && cleanupErr == nil {
			cleanupErr = fmt.Errorf("清理核心沙盒目录失败：%w", err)
		}
		return cleanupErr
	}

	for _, mount := range mounts {
		target := sandboxTarget(root, mount.target)
		if err := mountIntoSandbox(target, mount); err != nil {
			_ = cleanup()
			return "", nil, err
		}
		mounted = append(mounted, target)
	}

	return root, cleanup, nil
}

func sandboxRootTemplate() (string, error) {
	startTime, err := linuxProcessStartTime(os.Getpid())
	if err != nil {
		return "", fmt.Errorf("读取 service 进程启动时间失败：%w", err)
	}
	return fmt.Sprintf("%s%d-%s-*", sandboxRootPrefix, os.Getpid(), startTime), nil
}

func mountSandboxRoot(root string) error {
	if err := syscall.Mount(root, root, "", uintptr(syscall.MS_BIND), ""); err != nil {
		return fmt.Errorf("初始化核心沙盒根目录失败 %s：%w", root, err)
	}
	if err := makeSandboxMountPrivate(root); err != nil {
		_ = syscall.Unmount(root, syscall.MNT_DETACH)
		return fmt.Errorf("隔离核心沙盒根目录失败 %s：%w", root, err)
	}
	return nil
}

func prepareLinuxSandboxStaticLayout(root string) error {
	if err := os.MkdirAll(filepath.Join(root, "run"), 0o755); err != nil {
		return err
	}
	tmpDir := filepath.Join(root, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return err
	}
	if err := os.Chmod(tmpDir, 0o1777); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(root, "var"), 0o755); err != nil {
		return err
	}
	if err := os.Symlink("../run", filepath.Join(root, "var", "run")); err != nil && !os.IsExist(err) {
		return err
	}
	return nil
}

func cleanupStaleLinuxSandboxRoots() {
	roots, err := filepath.Glob(filepath.Join(os.TempDir(), sandboxRootPrefix+"*"))
	if err != nil {
		logSandboxCleanupError(fmt.Errorf("查找残留核心沙盒目录失败：%w", err))
		return
	}

	for _, root := range roots {
		if sandboxRootOwnerActive(root) {
			continue
		}
		if err := cleanupLinuxSandboxRoot(root); err != nil {
			logSandboxCleanupError(err)
		}
	}
}

func sandboxRootOwnerActive(root string) bool {
	name := strings.TrimPrefix(filepath.Base(root), sandboxRootPrefix)
	pidValue, rest, ok := strings.Cut(name, "-")
	if !ok {
		return false
	}
	startTime, _, ok := strings.Cut(rest, "-")
	if !ok {
		return false
	}
	pid, err := strconv.Atoi(pidValue)
	if err != nil || pid <= 0 {
		return false
	}
	actualStartTime, err := linuxProcessStartTime(pid)
	return err == nil && actualStartTime == startTime
}

func linuxProcessStartTime(pid int) (string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	closingParen := strings.LastIndexByte(string(data), ')')
	if closingParen < 0 {
		return "", fmt.Errorf("进程状态格式无效")
	}
	fields := strings.Fields(string(data[closingParen+1:]))
	const startTimeIndexAfterCommand = 19
	if len(fields) <= startTimeIndexAfterCommand {
		return "", fmt.Errorf("进程状态字段不足")
	}
	return fields[startTimeIndexAfterCommand], nil
}

func cleanupLinuxSandboxRoot(root string) error {
	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取残留核心沙盒目录失败 %s：%w", root, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() || info.Mode().Perm() != 0o700 {
		return nil
	}

	var cleanupErr error
	for _, mountPoint := range linuxMountPointsUnder(root) {
		if err := makeSandboxMountPrivate(mountPoint); err != nil {
			if !errors.Is(err, syscall.EINVAL) &&
				!errors.Is(err, syscall.ENOENT) &&
				cleanupErr == nil {
				cleanupErr = fmt.Errorf("隔离残留核心沙盒映射失败 %s：%w", mountPoint, err)
			}
			continue
		}
		if err := syscall.Unmount(mountPoint, syscall.MNT_DETACH); err != nil &&
			!errors.Is(err, syscall.EINVAL) &&
			!errors.Is(err, syscall.ENOENT) &&
			cleanupErr == nil {
			cleanupErr = fmt.Errorf("卸载残留核心沙盒映射失败 %s：%w", mountPoint, err)
		}
	}
	if err := os.RemoveAll(root); err != nil && cleanupErr == nil {
		cleanupErr = fmt.Errorf("清理残留核心沙盒目录失败 %s：%w", root, err)
	}
	return cleanupErr
}

func linuxMountPointsUnder(root string) []string {
	root = filepath.Clean(root)
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil
	}

	var mountPoints []string
	for line := range strings.SplitSeq(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}

		mountPoint := filepath.Clean(unescapeMountInfoPath(fields[4]))
		if pathWithin(mountPoint, root) {
			mountPoints = append(mountPoints, mountPoint)
		}
	}

	slices.SortFunc(mountPoints, func(a, b string) int {
		return strings.Count(b, string(os.PathSeparator)) - strings.Count(a, string(os.PathSeparator))
	})
	return mountPoints
}

func unescapeMountInfoPath(path string) string {
	replacer := strings.NewReplacer(
		`\040`, " ",
		`\011`, "\t",
		`\012`, "\n",
		`\134`, `\`,
	)
	return replacer.Replace(path)
}

func pathWithin(path string, root string) bool {
	if path == root {
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func mountIntoSandbox(target string, mount sandboxMount) error {
	if mount.proc {
		return mountKernelFilesystem(target, "proc", uintptr(syscall.MS_NOSUID|syscall.MS_NOEXEC|syscall.MS_NODEV))
	}

	if mount.file {
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		file, err := os.OpenFile(target, os.O_CREATE|os.O_RDWR, 0o600)
		if err != nil {
			return err
		}
		_ = file.Close()
	} else if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}

	flags := uintptr(syscall.MS_BIND | syscall.MS_REC)
	if err := syscall.Mount(mount.source, target, "", flags, ""); err != nil {
		return fmt.Errorf("映射沙盒路径失败 %s -> %s：%w", mount.source, mount.target, err)
	}
	if err := makeSandboxMountPrivate(target); err != nil {
		_ = syscall.Unmount(target, syscall.MNT_DETACH)
		return fmt.Errorf("隔离沙盒映射失败 %s：%w", mount.target, err)
	}

	if mount.readOnly {
		attr := &unix.MountAttr{Attr_set: unix.MOUNT_ATTR_RDONLY}
		if err := unix.MountSetattr(unix.AT_FDCWD, target, unix.AT_RECURSIVE, attr); err != nil {
			if err := makeSandboxMountTreeReadOnly(target); err != nil {
				_ = syscall.Unmount(target, syscall.MNT_DETACH)
				return fmt.Errorf("设置沙盒递归只读映射失败 %s：%w", mount.target, err)
			}
		}
	}

	return nil
}

func makeSandboxMountTreeReadOnly(target string) error {
	mountPoints := linuxMountPointsUnder(target)
	if len(mountPoints) == 0 {
		mountPoints = []string{target}
	}
	for _, mountPoint := range mountPoints {
		flags := uintptr(syscall.MS_BIND | syscall.MS_REMOUNT | syscall.MS_RDONLY)
		if err := syscall.Mount("", mountPoint, "", flags, ""); err != nil {
			return fmt.Errorf("只读重挂载 %s 失败：%w", mountPoint, err)
		}
	}
	return nil
}

func makeSandboxMountPrivate(target string) error {
	err := syscall.Mount("", target, "", uintptr(syscall.MS_PRIVATE|syscall.MS_REC), "")
	if errors.Is(err, syscall.EINVAL) {
		return syscall.Mount("", target, "", uintptr(syscall.MS_PRIVATE), "")
	}
	return err
}

func mountKernelFilesystem(target string, fsType string, flags uintptr) error {
	if err := os.MkdirAll(target, 0o555); err != nil {
		return err
	}
	if err := syscall.Mount(fsType, target, fsType, flags, ""); err != nil {
		return fmt.Errorf("挂载 %s 失败：%w", target, err)
	}
	if err := makeSandboxMountPrivate(target); err != nil {
		_ = syscall.Unmount(target, syscall.MNT_DETACH)
		return fmt.Errorf("隔离沙盒映射失败 %s：%w", target, err)
	}
	return nil
}

func sandboxMounts(config Config) ([]sandboxMount, error) {
	mounts := make([]sandboxMount, 0, 32)
	addMount := func(source string, readOnly bool, file bool) error {
		source, err := normalizeSandboxPath(source)
		if err != nil {
			return err
		}
		if _, err := os.Stat(source); err != nil {
			return err
		}
		mounts = append(mounts, sandboxMount{
			source:   source,
			target:   source,
			readOnly: readOnly,
			file:     file,
		})
		return nil
	}
	addMountIfExists := func(source string, readOnly bool, file bool) error {
		if _, err := os.Stat(source); os.IsNotExist(err) {
			return nil
		}
		return addMount(source, readOnly, file)
	}
	addReadOnlyPath := func(path string) error {
		path, err := normalizeSandboxPath(path)
		if err != nil {
			return err
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		return addMount(path, true, !info.IsDir())
	}
	addWritableDir := func(path string) error {
		path, err := writableSandboxDir(path)
		if err != nil {
			return err
		}
		return addMount(path, false, false)
	}
	addWritablePath := func(path string) error {
		path, err := normalizeSandboxPath(path)
		if err != nil {
			return err
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		return addMount(path, false, !info.IsDir())
	}
	for _, path := range config.WritablePaths {
		if filepath.Clean(path) == string(os.PathSeparator) {
			log.Printf("sandbox 的可写路径包含 /，文件系统隔离已关闭")
			return []sandboxMount{{
				source: "/",
				target: "/",
			}}, nil
		}
	}

	for _, dir := range []string{"/bin", "/sbin", "/usr", "/lib", "/lib64", "/etc", "/sys", "/nix/store"} {
		if err := addMountIfExists(dir, true, false); err != nil {
			return nil, err
		}
	}
	for _, dir := range []string{
		"/run/systemd/resolve",
		"/run/resolvconf",
		"/run/current-system",
		"/run/booted-system",
		"/run/wrappers",
	} {
		if err := addMountIfExists(dir, true, false); err != nil {
			return nil, err
		}
	}
	if err := addMount("/run/xtables.lock", false, true); err != nil {
		return nil, err
	}

	coreDir, err := sandboxDirForPath(config.ExecutablePath)
	if err != nil {
		return nil, err
	}
	coreReadOnly := pathWithin(coreDir, "/nix/store")
	if err := addMount(coreDir, coreReadOnly, false); err != nil {
		return nil, err
	}
	resolvedExecutable, err := filepath.EvalSymlinks(config.ExecutablePath)
	if err != nil {
		return nil, fmt.Errorf("解析核心可执行文件真实路径失败：%w", err)
	}
	resolvedCoreDir, err := sandboxDirForPath(resolvedExecutable)
	if err != nil {
		return nil, err
	}
	if resolvedCoreDir != coreDir {
		resolvedReadOnly := pathWithin(resolvedCoreDir, "/nix/store")
		if err := addMount(resolvedCoreDir, resolvedReadOnly, false); err != nil {
			return nil, err
		}
	}
	for _, path := range config.ReadOnlyPaths {
		if err := addReadOnlyPath(path); err != nil {
			return nil, err
		}
	}

	workingDir, err := sandboxDirForPath(config.WorkingDir)
	if err != nil {
		return nil, err
	}
	if workingDir != coreDir {
		if err := addMount(workingDir, true, false); err != nil {
			return nil, err
		}
	}
	for _, path := range config.WritablePaths {
		if err := addWritablePath(path); err != nil {
			return nil, &configError{err: fmt.Errorf("映射可信路径失败 %q：%w", path, err)}
		}
	}
	for _, path := range config.WritableDirs {
		if err := addWritableDir(path); err != nil {
			return nil, err
		}
	}

	mounts = append(mounts, sandboxMount{target: "/proc", proc: true})
	for _, dev := range []string{"/dev/null", "/dev/zero", "/dev/random", "/dev/urandom", "/dev/net/tun"} {
		if err := addMountIfExists(dev, false, true); err != nil {
			return nil, err
		}
	}

	return compactSandboxMounts(mounts), nil
}

func ensureXTablesLock() error {
	file, err := os.OpenFile("/run/xtables.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("准备 iptables 共享锁失败：%w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("关闭 iptables 共享锁失败：%w", err)
	}
	return nil
}

func compactSandboxMounts(mounts []sandboxMount) []sandboxMount {
	seen := make(map[string]int, len(mounts))
	result := make([]sandboxMount, 0, len(mounts))
	for _, mount := range mounts {
		if mount.target == "" {
			continue
		}
		mount.target = filepath.Clean(mount.target)
		if mount.source != "" {
			mount.source = filepath.Clean(mount.source)
		}
		if index, ok := seen[mount.target]; ok {
			existing := result[index]
			existing.readOnly = existing.readOnly && mount.readOnly
			existing.file = existing.file || mount.file
			existing.proc = existing.proc || mount.proc
			if existing.source == "" {
				existing.source = mount.source
			}
			result[index] = existing
			continue
		}
		seen[mount.target] = len(result)
		result = append(result, mount)
	}

	slices.SortStableFunc(result, func(a, b sandboxMount) int {
		return strings.Count(a.target, string(os.PathSeparator)) - strings.Count(b.target, string(os.PathSeparator))
	})
	return result
}

func writableSandboxDir(path string) (string, error) {
	path, err := normalizeSandboxPath(path)
	if err != nil {
		return "", err
	}

	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		path = filepath.Dir(path)
	}
	path = filepath.Clean(path)

	return path, nil
}

func sandboxDirForPath(path string) (string, error) {
	path, err := normalizeSandboxPath(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return path, nil
	}
	return filepath.Dir(path), nil
}

func sandboxTarget(root string, target string) string {
	return filepath.Join(root, strings.TrimPrefix(filepath.Clean(target), string(os.PathSeparator)))
}

func normalizeSandboxPath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("路径不能为空")
	}
	if !filepath.IsAbs(path) {
		absPath, err := filepath.Abs(path)
		if err != nil {
			return "", err
		}
		path = absPath
	}
	return filepath.Clean(path), nil
}

func logSandboxCleanupError(err error) {
	if err != nil {
		log.Printf("清理核心沙盒失败：%v", err)
	}
}
