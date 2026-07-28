//go:build !windows

package relay

import (
	"errors"
	"os"
)

func setPrivatePermissions(path string, mode os.FileMode) error {
	return os.Chmod(path, mode)
}

func setPrivateFilePermissions(file *os.File, mode os.FileMode) error {
	return file.Chmod(mode)
}

func isStorageLockContention(err error) bool {
	return errors.Is(err, os.ErrExist)
}
