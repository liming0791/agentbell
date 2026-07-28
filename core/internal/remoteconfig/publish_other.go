//go:build !windows

package remoteconfig

import "os"

func publishSidecar(temporaryPath, destination string) error {
	return os.Rename(temporaryPath, destination)
}
