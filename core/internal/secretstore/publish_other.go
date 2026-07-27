//go:build !windows

package secretstore

import (
	"os"
	"path/filepath"
)

func publishPrivateFile(temporaryPath, destination string) error {
	if err := os.Rename(temporaryPath, destination); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(destination))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
