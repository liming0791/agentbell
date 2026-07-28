//go:build windows

package relay

import (
	"errors"
	"os"
	"syscall"
)

const (
	windowsErrorSharingViolation syscall.Errno = 32
	windowsErrorLockViolation    syscall.Errno = 33
)

// Windows does not implement POSIX owner/group/other modes. Files and
// directories inherit the current user's DACL from AgentBell's state root;
// chmod would only toggle the DOS read-only attribute and races with other
// processes opening the same directory.
func setPrivatePermissions(string, os.FileMode) error {
	return nil
}

func setPrivateFilePermissions(*os.File, os.FileMode) error {
	return nil
}

func isStorageLockContention(err error) bool {
	return errors.Is(err, os.ErrExist) ||
		errors.Is(err, syscall.ERROR_ACCESS_DENIED) ||
		errors.Is(err, windowsErrorSharingViolation) ||
		errors.Is(err, windowsErrorLockViolation)
}
