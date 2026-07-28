//go:build !windows

package remoteconfig

import (
	"errors"
	"os"
)

func setSidecarPermissions(path string, mode os.FileMode) error {
	return os.Chmod(path, mode)
}

func setSidecarFilePermissions(file *os.File, mode os.FileMode) error {
	return file.Chmod(mode)
}

func isSidecarLockContention(err error) bool {
	return errors.Is(err, os.ErrExist)
}
