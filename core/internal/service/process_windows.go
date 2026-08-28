package service

import (
	"errors"

	"golang.org/x/sys/windows"
)

func serviceProcessAlive(pid int) (bool, error) {
	process, err := windows.OpenProcess(
		windows.PROCESS_QUERY_LIMITED_INFORMATION,
		false,
		uint32(pid),
	)
	if err == nil {
		_ = windows.CloseHandle(process)
		return true, nil
	}
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return false, nil
	}
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return true, nil
	}
	return false, err
}
