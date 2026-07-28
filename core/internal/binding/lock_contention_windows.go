//go:build windows

package binding

import (
	"errors"
	"os"
	"syscall"
)

const (
	windowsErrorSharingViolation syscall.Errno = 32
	windowsErrorLockViolation    syscall.Errno = 33
)

func isBindingLockContention(err error) bool {
	return errors.Is(err, os.ErrExist) ||
		errors.Is(err, syscall.ERROR_ACCESS_DENIED) ||
		errors.Is(err, windowsErrorSharingViolation) ||
		errors.Is(err, windowsErrorLockViolation)
}

// Binding state lives below the current user's private state directory and the
// lock inherits that directory's DACL. Windows does not implement POSIX mode
// bits; chmod would only manipulate the DOS read-only attribute.
func setBindingLockPermissions(*os.File) error {
	return nil
}
