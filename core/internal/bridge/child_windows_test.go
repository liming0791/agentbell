package bridge

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/windows"
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

func TestServiceChildUsesKillOnCloseJob(t *testing.T) {
	err := runServiceChild(
		context.Background(),
		"powershell.exe",
		[]string{
			"-NoProfile",
			"-NonInteractive",
			"-Command",
			"Start-Sleep -Milliseconds 50",
		},
		nil,
		io.Discard,
		io.Discard,
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestServiceChildJobClosesWithParent(t *testing.T) {
	if os.Getenv("AGENTBELL_JOB_TEST_PARENT") == "1" {
		err := runServiceChild(
			context.Background(),
			"powershell.exe",
			[]string{
				"-NoProfile",
				"-NonInteractive",
				"-Command",
				"[IO.File]::WriteAllText(" +
					"$env:AGENTBELL_JOB_TEST_PID_FILE, [string]$PID); " +
					"Start-Sleep -Seconds 30",
			},
			nil,
			io.Discard,
			io.Discard,
		)
		if err != nil {
			t.Fatal(err)
		}
		return
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pidFile := t.TempDir() + `\child.pid`
	parent := exec.Command(
		executable,
		"-test.run=^TestServiceChildJobClosesWithParent$",
	)
	parent.Env = append(
		os.Environ(),
		"AGENTBELL_JOB_TEST_PARENT=1",
		"AGENTBELL_JOB_TEST_PID_FILE="+pidFile,
	)
	parent.SysProcAttr = &windows.SysProcAttr{CreationFlags: createNoWindow}
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	parentWaited := false
	defer func() {
		if !parentWaited {
			_ = parent.Process.Kill()
			_ = parent.Wait()
		}
	}()

	var childPID int
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		raw, readErr := os.ReadFile(pidFile)
		if readErr == nil {
			childPID, err = strconv.Atoi(string(raw))
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if childPID == 0 {
		t.Fatal("service child PID was not published")
	}
	child, err := windows.OpenProcess(
		windows.PROCESS_QUERY_LIMITED_INFORMATION|
			windows.PROCESS_TERMINATE|
			windows.SYNCHRONIZE,
		false,
		uint32(childPID),
	)
	if err != nil {
		t.Fatalf("open service child %d: %v", childPID, err)
	}
	defer windows.CloseHandle(child)
	defer windows.TerminateProcess(child, 1)

	if err := parent.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = parent.Wait()
	parentWaited = true

	childDeadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(childDeadline) {
		status, waitErr := windows.WaitForSingleObject(child, 0)
		if waitErr != nil {
			t.Fatalf("wait for service child %d: %v", childPID, waitErr)
		}
		if status == windows.WAIT_OBJECT_0 {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("service child %d survived its bridge parent", childPID)
}
