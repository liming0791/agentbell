package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// Save validates the config and writes it atomically: a temporary file in the
// target directory is renamed over the destination. The file is created with
// mode 0600 and the parent directory with mode 0700.
func Save(path string, value *Config) error {
	if value == nil {
		return errors.New("config is nil")
	}
	if err := value.Validate(); err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	temporary, err := os.CreateTemp(directory, "config-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		os.Remove(temporaryPath)
		return err
	}
	if err := temporary.Close(); err != nil {
		os.Remove(temporaryPath)
		return err
	}
	if err := os.Chmod(temporaryPath, 0o600); err != nil {
		os.Remove(temporaryPath)
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		os.Remove(temporaryPath)
		return err
	}
	return nil
}
