package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

type desiredState struct {
	CoreShouldBeRunning bool          `json:"core_should_be_running"`
	Profile             LaunchProfile `json:"profile"`
	UpdatedAt           time.Time     `json:"updated_at"`
}

var desiredStateMu sync.Mutex

func desiredStatePath() string {
	return filepath.Join(serviceConfigDir(), "sparkle", "core", "desired_state.json")
}

func loadDesiredState() (desiredState, error) {
	path := desiredStatePath()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return desiredState{}, nil
	}
	if err != nil {
		return desiredState{}, fmt.Errorf("读取核心运行状态失败 %q：%w", path, err)
	}
	var state desiredState
	if err := json.Unmarshal(data, &state); err != nil {
		backup := path + fmt.Sprintf(".corrupt-%d", time.Now().UnixNano())
		if renameErr := os.Rename(path, backup); renameErr != nil {
			return desiredState{}, fmt.Errorf("解析核心运行状态失败：%v（隔离损坏文件失败：%w）", err, renameErr)
		}
		return desiredState{}, fmt.Errorf("解析核心运行状态失败：%w（已隔离到 %q）", err, backup)
	}
	return state, nil
}

func saveDesiredState(state desiredState) error {
	path := desiredStatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("创建核心状态目录失败：%w", err)
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("设置核心状态目录权限失败：%w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化核心运行状态失败：%w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".desired_state-*")
	if err != nil {
		return fmt.Errorf("创建核心状态临时文件失败：%w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("设置核心状态文件权限失败：%w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("写入核心运行状态失败：%w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("同步核心运行状态失败：%w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭核心状态临时文件失败：%w", err)
	}
	if err := replaceStateFile(tmpPath, path); err != nil {
		return fmt.Errorf("替换核心运行状态失败：%w", err)
	}
	return nil
}

func replaceStateFile(tempPath, path string) error {
	if runtime.GOOS == "windows" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return os.Rename(tempPath, path)
}

func persistDesiredState(running bool, profile LaunchProfile) error {
	desiredStateMu.Lock()
	defer desiredStateMu.Unlock()
	return saveDesiredState(desiredState{
		CoreShouldBeRunning: running,
		Profile:             profile,
		UpdatedAt:           time.Now().UTC(),
	})
}

func clearDesiredState() error {
	return persistDesiredState(false, LaunchProfile{})
}

// RestoreDesiredState starts the previously requested core, if one was recorded.
// Callers may log and continue when the recovery hint is stale or malformed.
func (cm *CoreManager) RestoreDesiredState() error {
	desiredStateMu.Lock()
	state, err := loadDesiredState()
	desiredStateMu.Unlock()
	if err != nil {
		return err
	}
	if !state.CoreShouldBeRunning {
		return nil
	}
	if err := cm.StartCoreWithProfile(&state.Profile); err != nil {
		return fmt.Errorf("恢复核心运行状态失败：%w", err)
	}
	return nil
}
