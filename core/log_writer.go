package core

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
)

const logTrimLowWatermarkRatio = 0.7

type coreLogSettings struct {
	path     string
	saveLogs bool
	maxBytes int64
	access   fileAccess
}

type boundedLogWriter struct {
	mutex     sync.Mutex
	file      *os.File
	path      string
	saveLogs  bool
	maxBytes  int64
	access    fileAccess
	closed    bool
	lastError string
}

func newBoundedLogWriter(settings coreLogSettings) *boundedLogWriter {
	return &boundedLogWriter{
		path:     settings.path,
		saveLogs: settings.saveLogs,
		maxBytes: settings.maxBytes,
		access:   settings.access,
	}
}

func (w *boundedLogWriter) Update(settings coreLogSettings) {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	if w.closed {
		return
	}

	if w.path != settings.path || !settings.saveLogs {
		if err := w.closeFileLocked(); err != nil {
			w.reportErrorLocked(err)
		}
	}

	w.path = settings.path
	w.saveLogs = settings.saveLogs
	w.maxBytes = settings.maxBytes
	w.access = settings.access
	w.lastError = ""

	if w.saveLogs && w.path != "" {
		if err := w.ensureOpenLocked(); err != nil {
			w.reportErrorLocked(err)
		} else if err := w.enforceLimitLocked(); err != nil {
			w.reportErrorLocked(errors.Join(err, w.closeFileLocked()))
		}
	}
}

func (w *boundedLogWriter) Write(p []byte) (int, error) {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	if w.closed || len(p) == 0 || !w.saveLogs || w.path == "" {
		return len(p), nil
	}

	if err := w.ensureOpenLocked(); err != nil {
		w.reportErrorLocked(err)
		return len(p), nil
	}

	if _, err := w.file.Write(p); err != nil {
		w.reportErrorLocked(errors.Join(err, w.closeFileLocked()))
		return len(p), nil
	}

	if err := w.enforceLimitLocked(); err != nil {
		w.reportErrorLocked(errors.Join(err, w.closeFileLocked()))
	}

	return len(p), nil
}

func (w *boundedLogWriter) Close() error {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	w.closed = true
	return w.closeFileLocked()
}

func (w *boundedLogWriter) ensureOpenLocked() error {
	if w.file != nil {
		return nil
	}
	if err := ensureCoreLogDir(filepath.Dir(w.path), w.access); err != nil {
		return fmt.Errorf("创建核心日志目录失败：%w", err)
	}

	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("打开核心日志文件失败：%w", err)
	}
	if err := applyCoreLogFileAccess(w.path, w.access); err != nil {
		_ = file.Close()
		return err
	}
	w.file = file
	return w.enforceLimitLocked()
}

func (w *boundedLogWriter) enforceLimitLocked() error {
	if w.maxBytes <= 0 || w.file == nil {
		return nil
	}

	info, err := w.file.Stat()
	if err != nil {
		return fmt.Errorf("检查核心日志大小失败：%w", err)
	}
	if info.Size() <= w.maxBytes {
		return nil
	}

	targetBytes := max(int64(float64(w.maxBytes)*logTrimLowWatermarkRatio), 1)

	if err := w.file.Close(); err != nil {
		w.file = nil
		return fmt.Errorf("关闭核心日志文件失败：%w", err)
	}
	w.file = nil

	content, err := readLogTail(w.path, info.Size(), targetBytes)
	if err != nil {
		_ = w.reopenLocked()
		return err
	}

	if err := os.WriteFile(w.path, content, 0o600); err != nil {
		_ = w.reopenLocked()
		return fmt.Errorf("裁剪核心日志文件失败：%w", err)
	}
	if err := applyCoreLogFileAccess(w.path, w.access); err != nil {
		_ = w.reopenLocked()
		return err
	}

	return w.reopenLocked()
}

func readLogTail(path string, fileSize int64, targetBytes int64) ([]byte, error) {
	if fileSize <= targetBytes {
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("读取核心日志文件失败：%w", err)
		}
		return content, nil
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开核心日志文件失败：%w", err)
	}
	defer file.Close()

	offset := fileSize - targetBytes
	content := make([]byte, targetBytes)
	n, err := file.ReadAt(content, offset)
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("读取核心日志文件尾部失败：%w", err)
	}
	content = content[:n]

	if index := bytes.IndexByte(content, '\n'); index >= 0 && index < len(content)-1 {
		content = content[index+1:]
	}

	return content, nil
}

func (w *boundedLogWriter) reopenLocked() error {
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("重新打开核心日志文件失败：%w", err)
	}
	if err := applyCoreLogFileAccess(w.path, w.access); err != nil {
		_ = file.Close()
		return err
	}
	w.file = file
	return nil
}

func (w *boundedLogWriter) closeFileLocked() error {
	if w.file == nil {
		return nil
	}

	err := w.file.Close()
	w.file = nil
	return err
}

func (w *boundedLogWriter) reportErrorLocked(err error) {
	message := err.Error()
	if message == w.lastError {
		return
	}
	w.lastError = message
	log.Printf("写入核心日志失败: %v", err)
}

func coreLogSettingsFromProfile(profile LaunchProfile, access fileAccess) coreLogSettings {
	saveLogs := true
	if profile.SaveLogs != nil {
		saveLogs = *profile.SaveLogs
	}
	return coreLogSettings{
		path:     profile.LogPath,
		saveLogs: saveLogs,
		maxBytes: maxLogFileSizeBytes(profile.MaxLogFileSizeMB),
		access:   access,
	}
}
