package core

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

var stateFileWriteMu sync.Mutex

func atomicWriteFile(path string, data []byte, fileMode os.FileMode) error {
	stateFileWriteMu.Lock()
	defer stateFileWriteMu.Unlock()

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".state-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(fileMode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := replaceStateFile(tmpPath, path); err != nil {
		return fmt.Errorf("替换状态文件失败：%w", err)
	}
	if err := syncStateDirectory(filepath.Dir(path)); err != nil {
		return fmt.Errorf("同步状态目录失败：%w", err)
	}
	return nil
}

func syncStateDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
