//go:build !windows

package binding

import (
	"errors"
	"os"
)

func isBindingLockContention(err error) bool {
	return errors.Is(err, os.ErrExist)
}

func setBindingLockPermissions(file *os.File) error {
	return file.Chmod(0o600)
}
