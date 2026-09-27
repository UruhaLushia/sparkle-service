package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化核心运行状态失败：%w", err)
	}
	return atomicWriteFile(path, data, 0o600)
}

func replaceStateFile(tempPath, path string) error {
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
