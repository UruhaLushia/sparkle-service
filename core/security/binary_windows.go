//go:build windows

package security

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func SecureBinary(corePath string, skipHardening bool) error {
	if skipHardening || os.Getenv("SPARKLE_SKIP_CORE_ACL_HARDENING") == "1" {
		return nil
	}

	if err := RestrictPath(filepath.Dir(corePath), windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT, true); err != nil {
		return fmt.Errorf("加固核心目录权限失败：%w", err)
	}
	if err := RestrictPath(corePath, windows.NO_INHERITANCE, true); err != nil {
		return fmt.Errorf("加固核心文件权限失败：%w", err)
	}

	return nil
}

func RestrictPath(path string, inheritance uint32, allowUsersReadExecute bool) error {
	currentSID, err := CurrentProcessSID()
	if err != nil {
		return err
	}
	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	adminSID, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}

	entries := []windows.EXPLICIT_ACCESS{
		accessEntry(currentSID, windows.TRUSTEE_IS_USER, inheritance, windows.GENERIC_ALL),
		accessEntry(systemSID, windows.TRUSTEE_IS_USER, inheritance, windows.GENERIC_ALL),
		accessEntry(adminSID, windows.TRUSTEE_IS_GROUP, inheritance, windows.GENERIC_ALL),
	}
	if allowUsersReadExecute {
		usersSID, err := windows.CreateWellKnownSid(windows.WinBuiltinUsersSid)
		if err != nil {
			return err
		}
		entries = append(entries, accessEntry(usersSID, windows.TRUSTEE_IS_GROUP, inheritance,
			windows.FILE_GENERIC_READ|windows.FILE_GENERIC_EXECUTE))
	}

	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return err
	}

	return windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		acl,
		nil,
	)
}

func accessEntry(sid *windows.SID, trusteeType windows.TRUSTEE_TYPE, inheritance uint32, permissions windows.ACCESS_MASK) windows.EXPLICIT_ACCESS {
	return windows.EXPLICIT_ACCESS{
		AccessPermissions: permissions,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       inheritance,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  trusteeType,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}
}

func CurrentProcessSID() (*windows.SID, error) {
	token := windows.GetCurrentProcessToken()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("读取当前进程 SID 失败：%w", err)
	}

	return user.User.Sid, nil
}
