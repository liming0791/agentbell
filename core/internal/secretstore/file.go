package secretstore

import (
	"errors"
	"io"
	"os"
	"path/filepath"
)

func writePrivateFile(path string, value []byte, goos string) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ErrUnsafeStorage
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return ErrUnsafeStorage
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrUnsafeStorage
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return ErrUnsafeStorage
	}
	if err := requirePrivateDirectory(directory, goos); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".agentbell-secret-*.tmp")
	if err != nil {
		return ErrUnsafeStorage
	}
	temporaryPath := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}
	if err := temporary.Chmod(0o600); err != nil {
		cleanup()
		return ErrUnsafeStorage
	}
	if _, err := temporary.Write(value); err != nil {
		cleanup()
		return ErrUnsafeStorage
	}
	if err := temporary.Sync(); err != nil {
		cleanup()
		return ErrUnsafeStorage
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return ErrUnsafeStorage
	}
	if err := publishPrivateFile(temporaryPath, path); err != nil {
		_ = os.Remove(temporaryPath)
		return ErrUnsafeStorage
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return ErrUnsafeStorage
	}
	return nil
}

func readPrivateFile(path, goos string, maximum int64) ([]byte, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, ErrUnsafeStorage
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, ErrUnsafeStorage
	}
	if goos != "windows" && info.Mode().Perm() != 0o600 {
		return nil, ErrUnsafeStorage
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrUnsafeStorage
	}
	defer file.Close()
	value, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		clear(value)
		return nil, ErrUnsafeStorage
	}
	if int64(len(value)) > maximum {
		clear(value)
		return nil, ErrInvalidSecret
	}
	return value, nil
}

func removePrivateFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ErrUnsafeStorage
	}
	if err := os.Remove(path); err != nil {
		return ErrUnsafeStorage
	}
	return nil
}

func requirePrivateDirectory(path, goos string) error {
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return ErrUnsafeStorage
	}
	if goos != "windows" && info.Mode().Perm()&0o077 != 0 {
		return ErrUnsafeStorage
	}
	return nil
}
