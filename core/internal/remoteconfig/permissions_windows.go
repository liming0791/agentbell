//go:build windows

package remoteconfig

import (
	"errors"
	"os"
	"syscall"
)

const (
	windowsErrorSharingViolation syscall.Errno = 32
	windowsErrorLockViolation    syscall.Errno = 33
)

// Sidecars live below the current user's platform config directory and inherit
// that directory's DACL. POSIX mode bits are not implemented on Windows;
// chmod would only manipulate the DOS read-only attribute.
func setSidecarPermissions(string, os.FileMode) error {
	return nil
}

func setSidecarFilePermissions(*os.File, os.FileMode) error {
	return nil
}

func isSidecarLockContention(err error) bool {
	return errors.Is(err, os.ErrExist) ||
		errors.Is(err, syscall.ERROR_ACCESS_DENIED) ||
		errors.Is(err, windowsErrorSharingViolation) ||
		errors.Is(err, windowsErrorLockViolation)
}
