//go:build !windows

package bridge

import "os/exec"

func configureBackgroundChild(_ *exec.Cmd) {}
