package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type restartRunner struct {
	calls   []managerCall
	errs    map[string]error
	outputs map[string][]byte
}

func (runner *restartRunner) Run(
	_ context.Context,
	name string,
	args ...string,
) ([]byte, error) {
	call := managerCall{name: name, args: append([]string(nil), args...)}
	runner.calls = append(runner.calls, call)
	key := callKeyForManager(call)
	if err := runner.errs[key]; err != nil {
		return runner.outputs[key], err
	}
	if output, ok := runner.outputs[key]; ok {
		return output, nil
	}
	return []byte("ok"), nil
}

func TestManagerBridgeDefinitionsAreStable(t *testing.T) {
	root := t.TempDir()
	bridge := filepath.Join(root, "stable", "agentbell-bridge")
	manager := &Manager{
		GOOS:             "darwin",
		Executable:       filepath.Join(root, "versions", "v1", "agentbell"),
		BridgeExecutable: bridge,
		ServiceMode:      ServiceModeBridge,
		HomeDir:          filepath.Join(root, "home"),
		ConfigDir:        filepath.Join(root, "config"),
		LogDir:           filepath.Join(root, "logs"),
		UID:              "501",
	}

	plistFirst, err := manager.plist()
	if err != nil {
		t.Fatal(err)
	}
	plistSecond, err := manager.plist()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(plistFirst, plistSecond) {
		t.Fatal("bridge LaunchAgent definition is not byte-stable")
	}
	plistText := string(plistFirst)
	if !strings.Contains(plistText, xmlText(bridge)) ||
		!strings.Contains(plistText, "<string>service-v1</string>") ||
		strings.Contains(plistText, "<string>service</string>") {
		t.Fatalf("unexpected bridge plist:\n%s", plistText)
	}

	manager.GOOS = "linux"
	unitFirst, err := manager.systemdUnit()
	if err != nil {
		t.Fatal(err)
	}
	unitSecond, err := manager.systemdUnit()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(unitFirst, unitSecond) ||
		!strings.Contains(string(unitFirst), systemdQuote(bridge)+" service-v1") ||
		strings.Contains(string(unitFirst), "service run --foreground") {
		t.Fatalf("unexpected bridge systemd unit:\n%s", unitFirst)
	}
	desktopFirst, err := manager.xdgDesktopEntry()
	if err != nil {
		t.Fatal(err)
	}
	desktopSecond, err := manager.xdgDesktopEntry()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(desktopFirst, desktopSecond) ||
		!strings.Contains(string(desktopFirst), desktopExecArg(bridge)+" service-v1") ||
		strings.Contains(string(desktopFirst), "service run --foreground") {
		t.Fatalf("unexpected bridge XDG entry:\n%s", desktopFirst)
	}

	manager.GOOS = "windows"
	manager.BridgeExecutable = `C:\Users\test\AppData\Local\AgentBell\agentbell-bridge.exe`
	actionFirst, err := manager.windowsTaskAction()
	if err != nil {
		t.Fatal(err)
	}
	actionSecond, err := manager.windowsTaskAction()
	if err != nil {
		t.Fatal(err)
	}
	if actionFirst != actionSecond ||
		actionFirst != `"C:\Users\test\AppData\Local\AgentBell\agentbell-bridge.exe" service-v1` {
		t.Fatalf("unexpected bridge task action: %q", actionFirst)
	}
}

func TestManagerRejectsInvalidBridgeDefinition(t *testing.T) {
	manager, _ := testManager(t)
	manager.ServiceMode = ServiceModeBridge
	manager.BridgeExecutable = "relative/agentbell-bridge"
	if _, err := manager.Install(context.Background(), true); err == nil ||
		!strings.Contains(err.Error(), "bridge") {
		t.Fatalf("expected invalid bridge path error, got %v", err)
	}
	manager.BridgeExecutable = filepath.Join(manager.HomeDir, "bridge\ninvalid")
	if _, err := manager.Install(context.Background(), true); err == nil {
		t.Fatal("expected unsafe bridge path error")
	}
}

func TestManagerRestartLaunchdAndVerifiesStatus(t *testing.T) {
	manager, _ := testManager(t)
	runner := &restartRunner{errs: map[string]error{}, outputs: map[string][]byte{}}
	manager.Runner = runner
	result := manager.launchdResult()
	if err := os.MkdirAll(filepath.Dir(result.DefinitionPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(result.DefinitionPath, []byte("installed"), 0o600); err != nil {
		t.Fatal(err)
	}

	restarted, err := manager.Restart(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !restarted.Installed || !restarted.Loaded || !restarted.Running ||
		!restarted.Changed {
		t.Fatalf("unexpected restart result: %#v", restarted)
	}
	want := []string{
		"launchctl kickstart -k gui/501/com.agentbell.service",
		"launchctl print gui/501/com.agentbell.service",
	}
	if got := managerCallKeys(runner.calls); !equalStrings(got, want) {
		t.Fatalf("unexpected restart calls: %#v", got)
	}
	for _, call := range runner.calls {
		if call.name == "kill" || call.name == "pkill" {
			t.Fatalf("restart used an unverified PID: %#v", runner.calls)
		}
	}

	runner.calls = nil
	runner.errs["launchctl print gui/501/com.agentbell.service"] = errors.New("not running")
	if _, err := manager.Restart(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "verify") {
		t.Fatalf("expected launchd verification error, got %v", err)
	}
}

func TestManagerRestartSystemdAndVerifiesActive(t *testing.T) {
	root := t.TempDir()
	runner := &restartRunner{errs: map[string]error{}, outputs: map[string][]byte{}}
	manager := &Manager{
		GOOS:       "linux",
		Executable: filepath.Join(root, "agentbell"),
		HomeDir:    filepath.Join(root, "home"),
		ConfigDir:  filepath.Join(root, "config"),
		LogDir:     filepath.Join(root, "logs"),
		Runner:     runner,
		LookPath: func(string) (string, error) {
			return "/usr/bin/systemctl", nil
		},
	}
	result := manager.linuxResult(backendSystemd)
	if err := os.MkdirAll(filepath.Dir(result.DefinitionPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(result.DefinitionPath, []byte("installed"), 0o600); err != nil {
		t.Fatal(err)
	}

	restarted, err := manager.Restart(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !restarted.Installed || !restarted.Loaded || !restarted.Running ||
		!restarted.Changed {
		t.Fatalf("unexpected restart result: %#v", restarted)
	}
	want := []string{
		"systemctl --user restart agentbell.service",
		"systemctl --user is-enabled agentbell.service",
		"systemctl --user is-active agentbell.service",
	}
	if got := managerCallKeys(runner.calls); !equalStrings(got, want) {
		t.Fatalf("unexpected restart calls: %#v", got)
	}

	runner.calls = nil
	runner.errs["systemctl --user is-active agentbell.service"] = errors.New("inactive")
	if _, err := manager.Restart(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "verify") {
		t.Fatalf("expected systemd verification error, got %v", err)
	}
}

func TestManagerRestartWindowsTaskAndVerifiesRunning(t *testing.T) {
	stateDir := `C:\Users\test\AppData\Local\AgentBell\state`
	lockPath := filepath.Join(stateDir, "queue", "service.lock")
	runner := &restartRunner{
		errs: map[string]error{},
		outputs: map[string][]byte{
			windowsWaitStateCallKey(): []byte("Running\r\n"),
		},
	}
	manager := &Manager{
		GOOS:       "windows",
		Executable: `C:\Program Files\AgentBell\agentbell.exe`,
		HomeDir:    `C:\Users\test`,
		LogDir:     `C:\Users\test\AppData\Local\AgentBell\logs`,
		StateDir:   stateDir,
		Runner:     runner,
	}
	restarted, err := manager.Restart(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !restarted.Installed || !restarted.Loaded || !restarted.Running ||
		!restarted.Changed {
		t.Fatalf("unexpected restart result: %#v", restarted)
	}
	want := []string{
		`schtasks.exe /Query /TN \AgentBell\AgentBell`,
		`schtasks.exe /End /TN \AgentBell\AgentBell`,
		windowsQuiesceCallKey(lockPath),
		`schtasks.exe /Run /TN \AgentBell\AgentBell`,
		windowsWaitStateCallKey(),
	}
	if got := managerCallKeys(runner.calls); !equalStrings(got, want) {
		t.Fatalf("unexpected restart calls: %#v", got)
	}

	runner.calls = nil
	runner.errs[windowsQuiesceCallKey(lockPath)] = errors.New("old Core still running")
	if _, err := manager.Restart(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "wait for AgentBell Windows task to stop") {
		t.Fatalf("expected Windows quiesce error, got %v", err)
	}
	for _, call := range runner.calls {
		if callKeyForManager(call) == `schtasks.exe /Run /TN \AgentBell\AgentBell` {
			t.Fatalf("Windows task restarted before the old Core exited: %#v", runner.calls)
		}
	}

	delete(runner.errs, windowsQuiesceCallKey(lockPath))
	runner.calls = nil
	runner.outputs[windowsWaitStateCallKey()] = []byte("Ready")
	if _, err := manager.Restart(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "verify") {
		t.Fatalf("expected Windows verification error, got %v", err)
	}
}

func TestWindowsTaskWaitScriptIsBoundedInOnePowerShell(t *testing.T) {
	for _, expected := range []string{
		"AddSeconds(5)",
		"Start-Sleep -Milliseconds 100",
		"$state -eq 'Running'",
	} {
		if !strings.Contains(windowsTaskWaitStateScript, expected) {
			t.Fatalf("Windows task wait script missing %q", expected)
		}
	}
	if strings.Contains(windowsTaskStateScript, "Start-Sleep") {
		t.Fatal("ordinary Windows task status unexpectedly polls")
	}
	for _, expected := range []string{
		"AddSeconds(5)",
		"Start-Sleep -Milliseconds 100",
		"Get-Process -Id $lockOwnerPid",
		"$state -ne 'Running' -and -not $lockOwnerAlive",
	} {
		if !strings.Contains(windowsTaskQuiesceScript, expected) {
			t.Fatalf("Windows task quiesce script missing %q", expected)
		}
	}
	quotedPath := `C:\Users\Agent'Bell\state\queue\service.lock`
	quiesceArgs := windowsTaskQuiesceArgs(quotedPath)
	if len(quiesceArgs) != 4 ||
		!strings.Contains(
			quiesceArgs[3],
			`$LockPath = 'C:\Users\Agent''Bell\state\queue\service.lock';`,
		) {
		t.Fatalf("Windows lock path was not safely embedded: %#v", quiesceArgs)
	}
}

func TestManagerRestartXDGRefusesUnsafeProcessGuessing(t *testing.T) {
	root := t.TempDir()
	runner := &restartRunner{errs: map[string]error{}, outputs: map[string][]byte{}}
	manager := &Manager{
		GOOS:       "linux",
		Executable: filepath.Join(root, "agentbell"),
		HomeDir:    filepath.Join(root, "home"),
		ConfigDir:  filepath.Join(root, "config"),
		LogDir:     filepath.Join(root, "logs"),
		Runner:     runner,
		LookPath: func(string) (string, error) {
			return "", errors.New("missing")
		},
	}
	result := manager.linuxResult(backendXDG)
	if err := os.MkdirAll(filepath.Dir(result.DefinitionPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(result.DefinitionPath, []byte("installed"), 0o600); err != nil {
		t.Fatal(err)
	}

	restarted, err := manager.Restart(context.Background())
	if !errors.Is(err, ErrRestartUnsupported) {
		t.Fatalf("expected explicit XDG restart error, got %#v err=%v", restarted, err)
	}
	if !restarted.Installed || !restarted.Loaded || restarted.Running ||
		restarted.Changed ||
		!strings.Contains(restarted.Message, "next desktop login") {
		t.Fatalf("unexpected XDG restart result: %#v", restarted)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("XDG restart guessed a process: %#v", runner.calls)
	}
}

func managerCallKeys(calls []managerCall) []string {
	result := make([]string, 0, len(calls))
	for _, call := range calls {
		result = append(result, callKeyForManager(call))
	}
	return result
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
