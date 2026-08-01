package bridge

import (
	"os/exec"
	"testing"
)

func TestConfigureBackgroundChildSuppressesWindowsConsole(t *testing.T) {
	command := exec.Command("agentbell.exe", "service", "run", "--foreground")

	configureBackgroundChild(command)

	if command.SysProcAttr == nil {
		t.Fatal("expected Windows process attributes")
	}
	if !command.SysProcAttr.HideWindow {
		t.Fatal("expected the child window to be hidden")
	}
	if command.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Fatalf(
			"expected CREATE_NO_WINDOW in creation flags %#x",
			command.SysProcAttr.CreationFlags,
		)
	}
}
