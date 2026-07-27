//go:build !windows

package config

import (
	"errors"
	"os"
)

func isConfigLockContention(err error) bool {
	return errors.Is(err, os.ErrExist)
}
