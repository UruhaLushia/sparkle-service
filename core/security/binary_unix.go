//go:build !windows

package security

import (
	"fmt"
	"os"
	"path/filepath"
)

func SecureBinary(corePath string, skipHardening bool) error {
	if skipHardening || os.Getenv("SPARKLE_SKIP_CORE_ACL_HARDENING") == "1" {
		return nil
	}

	if err := removeGroupAndOtherWrite(filepath.Dir(corePath)); err != nil {
		return fmt.Errorf("加固核心目录权限失败：%w", err)
	}
	if err := removeGroupAndOtherWrite(corePath); err != nil {
		return fmt.Errorf("加固核心文件权限失败：%w", err)
	}

	return nil
}

func removeGroupAndOtherWrite(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}

	mode := info.Mode()
	if mode&0o022 == 0 {
		return nil
	}

	return os.Chmod(path, mode&^0o022)
}
