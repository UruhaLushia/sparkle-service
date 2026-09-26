package route

import (
	"context"
	"errors"
	"fmt"
	"github.com/UruhaLushia/sparkle-service/log"
	"github.com/UruhaLushia/sparkle-service/route/auth"
	"github.com/UruhaLushia/sparkle-service/route/coreapi"
	"github.com/UruhaLushia/sparkle-service/route/pipectx"
	"github.com/UruhaLushia/sparkle-service/route/sysproxyapi"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/UruhaLushia/sparkle-service/listen"
)

const serverShutdownTimeout = 3 * time.Second

var (
	unixServer *http.Server
	pipeServer *http.Server
	serverMu   sync.Mutex
)

func GetConfigDir() string {
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

func Start(addr string) error {
	userDataDir := GetConfigDir()

	keyDir := filepath.Join(userDataDir, "sparkle", "keys")

	if err := auth.InitKeyManager(keyDir); err != nil {
		log.Printf("警告: 初始化密钥管理器失败: %v", err)
	}

	km := auth.GetKeyManager()
	if km.IsInitialized() {
		log.Println("密钥管理器已初始化")
	} else {
		log.Println("警告：密钥管理器未初始化")
	}
	if km.HasAuthorizedPrincipal() {
		log.Println("请求方身份绑定已启用")
	} else {
		log.Println("警告：请求方身份绑定未启用")
	}
	if err := coreapi.RestoreDesiredState(); err != nil {
		log.Printf("警告：恢复核心运行状态失败: %v", err)
	}

	var err error
	if runtime.GOOS == "windows" {
		err = startServer(addr, StartPipe)
	} else {
		err = startServer(addr, StartUnix)
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func Stop() error {
	var errs []error
	sysproxyapi.StopGuard()
	if err := coreapi.Stop(); err != nil {
		errs = append(errs, fmt.Errorf("停止核心失败：%w", err))
	}
	if err := closeServers(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func startServer(addr string, startFunc func(string) error) error {
	if err := closeServers(); err != nil {
		return err
	}

	if len(addr) > 0 {
		if runtime.GOOS != "windows" {
			dir := filepath.Dir(addr)
			if err := ensureDirExists(dir); err != nil {
				return err
			}

			if err := syscall.Unlink(addr); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("unlink 错误：%w", err)
			}
		}

		if err := startFunc(addr); err != nil {
			return err
		}
	}
	return nil
}

func closeServers() error {
	serverMu.Lock()
	servers := []*http.Server{unixServer, pipeServer}
	unixServer = nil
	pipeServer = nil
	serverMu.Unlock()

	var errs []error
	for _, server := range servers {
		if server == nil {
			continue
		}
		if err := shutdownServer(server); err != nil {
			errs = append(errs, fmt.Errorf("关闭服务监听失败：%w", err))
		}
	}
	return errors.Join(errs...)
}

func shutdownServer(server *http.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), serverShutdownTimeout)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		if closeErr := server.Close(); closeErr != nil && !errors.Is(closeErr, http.ErrServerClosed) {
			return errors.Join(err, closeErr)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
	}
	return nil
}

func ensureDirExists(dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("目录创建错误：%w", err)
		}
	}
	return nil
}

func StartHTTP(addr string) error {
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("http 监听错误：%w", err)
	}
	log.Printf("http 监听地址: %s", addr)
	server := &http.Server{
		Handler: router(),
	}
	return server.Serve(l)
}

func StartUnix(addr string) error {
	l, err := net.Listen("unix", addr)
	if err != nil {
		return fmt.Errorf("unix 监听错误：%w", err)
	}
	if uid, ok := auth.GetKeyManager().GetAuthorizedUID(); ok {
		if err := os.Chown(addr, int(uid), -1); err != nil {
			_ = l.Close()
			return fmt.Errorf("设置 unix socket 所有者失败：%w", err)
		}
		if err := os.Chmod(addr, 0o600); err != nil {
			_ = l.Close()
			return fmt.Errorf("设置 unix socket 权限失败：%w", err)
		}
	} else if err := os.Chmod(addr, 0o600); err != nil {
		_ = l.Close()
		return fmt.Errorf("设置 unix socket 权限失败：%w", err)
	}
	log.Printf("unix 监听地址: %s", l.Addr().String())

	server := &http.Server{
		Handler: router(),
	}
	pipectx.ConfigureServer(server)
	serverMu.Lock()
	unixServer = server
	serverMu.Unlock()
	return server.Serve(l)
}

func StartPipe(addr string) error {
	pipeSDDL := ""
	if sid, ok := auth.GetKeyManager().GetAuthorizedSID(); ok {
		pipeSDDL = fmt.Sprintf("D:PAI(A;OICI;GWGR;;;%s)(A;OICI;GWGR;;;SY)", sid)
	}

	l, err := listen.ListenNamedPipe(addr, pipeSDDL)
	if err != nil {
		return fmt.Errorf("pipe 监听错误：%w", err)
	}
	log.Printf("pipe 监听地址: %s", l.Addr().String())

	server := &http.Server{
		Handler: router(),
	}
	pipectx.ConfigureServer(server)
	serverMu.Lock()
	pipeServer = server
	serverMu.Unlock()
	return server.Serve(l)
}
