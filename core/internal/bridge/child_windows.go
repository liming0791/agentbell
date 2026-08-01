package bridge

import (
	"os/exec"
	"syscall"
)

// CREATE_NO_WINDOW prevents the console Core launched by the GUI-subsystem
// stable bridge from allocating its own console window.
const createNoWindow = 0x08000000

func configureBackgroundChild(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
}
