package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type managerCall struct {
	name string
	args []string
}

type fakeManagerRunner struct {
	calls   []managerCall
	errs    map[string]error
	outputs map[string][]byte
}

func (runner *fakeManagerRunner) Run(
	_ context.Context,
	name string,
	args ...string,
) ([]byte, error) {
	runner.calls = append(runner.calls, managerCall{name: name, args: append([]string(nil), args...)})
	key := name + " " + strings.Join(args, " ")
	if err := runner.errs[key]; err != nil {
		return []byte("fake failure"), err
	}
	if output, ok := runner.outputs[key]; ok {
		return output, nil
	}
	return []byte("ok"), nil
}

func testManager(t *testing.T) (*Manager, *fakeManagerRunner) {
	t.Helper()
	root := t.TempDir()
	runner := &fakeManagerRunner{errs: map[string]error{}}
	manager := &Manager{
		GOOS:        "darwin",
		Executable:  filepath.Join(root, "AgentBell & Core"),
		HomeDir:     filepath.Join(root, "home"),
		LogDir:      filepath.Join(root, "logs"),
		LarkCLIPath: filepath.Join(root, "node & tools", "lark-cli"),
		UID:         "501",
		Runner:      runner,
	}
	return manager, runner
}

func TestManagerInstallWritesAndLoadsLaunchAgent(t *testing.T) {
	manager, runner := testManager(t)
	result, err := manager.Install(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Installed || !result.Loaded || !result.Changed {
		t.Fatalf("unexpected install result: %#v", result)
	}
	raw, err := os.ReadFile(result.PlistPath)
	if err != nil {
		t.Fatal(err)
	}
	plist := string(raw)
	for _, expected := range []string{
		"com.agentbell.service",
		"AgentBell &amp; Core",
		"<key>HOME</key><string>" + manager.HomeDir + "</string>",
		"node &amp; tools",
		"service.stdout.log",
		"service.stderr.log",
		"<key>KeepAlive</key><true/>",
	} {
		if !strings.Contains(plist, expected) {
			t.Fatalf("plist missing %q:\n%s", expected, plist)
		}
	}
	info, err := os.Stat(result.PlistPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("plist mode = %o, want 600", info.Mode().Perm())
	}
	if len(runner.calls) != 3 ||
		runner.calls[0].name != "launchctl" ||
		runner.calls[0].args[0] != "bootout" ||
		runner.calls[1].args[0] != "bootstrap" ||
		runner.calls[2].args[0] != "kickstart" {
		t.Fatalf("unexpected launchctl calls: %#v", runner.calls)
	}

	result, err = manager.Install(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed {
		t.Fatal("identical LaunchAgent install should be idempotent")
	}
}

func TestManagerDryRunDoesNotWriteOrInvokeLaunchctl(t *testing.T) {
	manager, runner := testManager(t)
	uninstall, err := manager.Uninstall(context.Background(), true)
	if err != nil || uninstall.Changed ||
		!strings.Contains(uninstall.Message, "not installed") {
		t.Fatalf("unexpected empty uninstall plan: %#v err=%v", uninstall, err)
	}
	result, err := manager.Install(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Installed || !result.Changed || result.Loaded {
		t.Fatalf("unexpected dry-run result: %#v", result)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("dry-run invoked launchctl: %#v", runner.calls)
	}
	if _, err := os.Stat(result.PlistPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run wrote plist: %v", err)
	}
}

func TestManagerStatusAndUninstall(t *testing.T) {
	manager, runner := testManager(t)
	installed, err := manager.Install(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	runner.calls = nil

	status, err := manager.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Installed || !status.Loaded {
		t.Fatalf("unexpected loaded status: %#v", status)
	}

	runner.errs["launchctl print gui/501/com.agentbell.service"] = errors.New("not loaded")
	status, err = manager.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Installed || status.Loaded {
		t.Fatalf("unexpected unloaded status: %#v", status)
	}

	removed, err := manager.Uninstall(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if removed.Installed || removed.Loaded || !removed.Changed {
		t.Fatalf("unexpected uninstall result: %#v", removed)
	}
	if _, err := os.Stat(installed.PlistPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plist was not removed: %v", err)
	}
}

func TestManagerValidationAndBootstrapFailure(t *testing.T) {
	manager, runner := testManager(t)
	manager.GOOS = "plan9"
	if _, err := manager.Install(context.Background(), true); err == nil {
		t.Fatal("expected unsupported platform error")
	}

	manager.GOOS = "darwin"
	manager.Executable = "relative"
	if _, err := manager.Install(context.Background(), true); err == nil {
		t.Fatal("expected relative path error")
	}

	manager.Executable = filepath.Join(manager.HomeDir, "agentbell")
	key := "launchctl bootstrap gui/501 " +
		filepath.Join(manager.HomeDir, "Library", "LaunchAgents", launchAgentLabel+".plist")
	runner.errs[key] = errors.New("bootstrap failed")
	if _, err := manager.Install(context.Background(), false); err == nil ||
		!strings.Contains(err.Error(), "load AgentBell LaunchAgent") {
		t.Fatalf("expected bootstrap error, got %v", err)
	}
}

func TestLinuxSystemdManagerInstallStatusAndUninstall(t *testing.T) {
	root := t.TempDir()
	runner := &fakeManagerRunner{errs: map[string]error{}}
	manager := &Manager{
		GOOS:        "linux",
		Executable:  filepath.Join(root, "AgentBell Core"),
		HomeDir:     filepath.Join(root, "home"),
		ConfigDir:   filepath.Join(root, "config"),
		LogDir:      filepath.Join(root, "logs"),
		LarkCLIPath: filepath.Join(root, "node tools", "lark-cli"),
		Runner:      runner,
		LookPath: func(name string) (string, error) {
			if name == "systemctl" {
				return "/usr/bin/systemctl", nil
			}
			return "", errors.New("missing")
		},
	}

	installed, err := manager.Install(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if installed.Backend != backendSystemd || !installed.Installed ||
		!installed.Loaded || !installed.Running || !installed.Changed {
		t.Fatalf("unexpected systemd install: %#v", installed)
	}
	raw, err := os.ReadFile(installed.DefinitionPath)
	if err != nil {
		t.Fatal(err)
	}
	unit := string(raw)
	for _, expected := range []string{
		"ExecStart=",
		"AgentBell Core",
		"service run --foreground",
		"Restart=always",
		"node tools",
		"StandardError=",
		"WantedBy=default.target",
	} {
		if !strings.Contains(unit, expected) {
			t.Fatalf("unit missing %q:\n%s", expected, unit)
		}
	}
	if len(runner.calls) < 3 ||
		callKeyForManager(runner.calls[len(runner.calls)-2]) !=
			"systemctl --user daemon-reload" ||
		callKeyForManager(runner.calls[len(runner.calls)-1]) !=
			"systemctl --user enable --now agentbell.service" {
		t.Fatalf("unexpected systemctl calls: %#v", runner.calls)
	}

	status, err := manager.Status(context.Background())
	if err != nil || !status.Installed || !status.Loaded || !status.Running {
		t.Fatalf("unexpected systemd status: %#v err=%v", status, err)
	}
	legacyAutostart := filepath.Join(
		manager.ConfigDir,
		"autostart",
		xdgAutostartName,
	)
	if err := os.MkdirAll(filepath.Dir(legacyAutostart), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyAutostart, []byte("[Desktop Entry]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	removed, err := manager.Uninstall(context.Background(), false)
	if err != nil || removed.Installed || !removed.Changed {
		t.Fatalf("unexpected systemd uninstall: %#v err=%v", removed, err)
	}
	if _, err := os.Stat(installed.DefinitionPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("systemd unit was not removed: %v", err)
	}
	if _, err := os.Stat(legacyAutostart); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy XDG entry was not removed: %v", err)
	}
}

func TestLinuxFallsBackToXDGAutostart(t *testing.T) {
	root := t.TempDir()
	runner := &fakeManagerRunner{errs: map[string]error{}}
	manager := &Manager{
		GOOS:       "linux",
		Executable: filepath.Join(root, "agentbell"),
		HomeDir:    filepath.Join(root, "home"),
		ConfigDir:  filepath.Join(root, "config"),
		LogDir:     filepath.Join(root, "logs"),
		Runner:     runner,
		LookPath: func(string) (string, error) {
			return "", errors.New("systemctl missing")
		},
	}
	result, err := manager.Install(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Backend != backendXDG || !result.Installed || !result.Loaded ||
		result.Running {
		t.Fatalf("unexpected XDG install: %#v", result)
	}
	raw, err := os.ReadFile(result.DefinitionPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "[Desktop Entry]") ||
		!strings.Contains(string(raw), "service run --foreground") {
		t.Fatalf("invalid XDG entry: %s", raw)
	}
	for _, call := range runner.calls {
		if call.name == "systemctl" {
			t.Fatalf("XDG fallback invoked systemctl: %#v", runner.calls)
		}
	}
}

func TestLinuxDryRunProbesUserManagerAndPredictsXDGFallback(t *testing.T) {
	root := t.TempDir()
	runner := &fakeManagerRunner{errs: map[string]error{
		"systemctl --user show-environment": errors.New("no user manager"),
	}}
	manager := &Manager{
		GOOS:       "linux",
		Executable: filepath.Join(root, "agentbell"),
		HomeDir:    filepath.Join(root, "home"),
		ConfigDir:  filepath.Join(root, "config"),
		LogDir:     filepath.Join(root, "logs"),
		Runner:     runner,
		LookPath: func(name string) (string, error) {
			if name == "systemctl" {
				return "/usr/bin/systemctl", nil
			}
			return "", errors.New("missing")
		},
	}
	result, err := manager.Install(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if result.Backend != backendXDG || !result.Changed ||
		!strings.Contains(result.DefinitionPath, "autostart") {
		t.Fatalf("dry-run did not predict XDG fallback: %#v", result)
	}
	if len(runner.calls) != 1 ||
		callKeyForManager(runner.calls[0]) != "systemctl --user show-environment" {
		t.Fatalf("unexpected dry-run probe calls: %#v", runner.calls)
	}
	if _, err := os.Stat(result.DefinitionPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run wrote XDG entry: %v", err)
	}
}

func TestWindowsTaskManagerInstallStatusAndUninstall(t *testing.T) {
	runner := &fakeManagerRunner{
		errs: map[string]error{
			`schtasks.exe /Query /TN \AgentBell\AgentBell`: errors.New("task not found"),
		},
		outputs: map[string][]byte{
			windowsStateCallKey(): []byte("Running\r\n"),
		},
	}
	manager := &Manager{
		GOOS:        "windows",
		Executable:  `C:\Program Files\AgentBell\agentbell.exe`,
		HomeDir:     `C:\Users\test`,
		LogDir:      `C:\Users\test\AppData\Local\AgentBell\logs`,
		LarkCLIPath: `C:\Users\test\AppData\Roaming\npm\lark-cli.cmd`,
		Runner:      runner,
	}
	installed, err := manager.Install(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if installed.Backend != backendTask || !installed.Installed ||
		!installed.Loaded || !installed.Running || !installed.Changed {
		t.Fatalf("unexpected task install: %#v", installed)
	}
	var createCall *managerCall
	for index := range runner.calls {
		if len(runner.calls[index].args) > 0 && runner.calls[index].args[0] == "/Create" {
			createCall = &runner.calls[index]
			break
		}
	}
	if createCall == nil {
		t.Fatalf("missing schtasks create call: %#v", runner.calls)
	}
	action := ""
	for index, value := range createCall.args {
		if value == "/TR" && index+1 < len(createCall.args) {
			action = createCall.args[index+1]
		}
	}
	if !strings.Contains(action, `"C:\Program Files\AgentBell\agentbell.exe"`) ||
		!strings.Contains(action, "service run --foreground") {
		t.Fatalf("unexpected task action: %q", action)
	}

	delete(runner.errs, `schtasks.exe /Query /TN \AgentBell\AgentBell`)
	runner.outputs[windowsStateCallKey()] = []byte("Running\r\n")
	status, err := manager.Status(context.Background())
	if err != nil || !status.Installed || !status.Loaded || !status.Running {
		t.Fatalf("unexpected task status: %#v err=%v", status, err)
	}
	removed, err := manager.Uninstall(context.Background(), false)
	if err != nil || removed.Installed || !removed.Changed {
		t.Fatalf("unexpected task uninstall: %#v err=%v", removed, err)
	}
}

func callKeyForManager(call managerCall) string {
	return call.name + " " + strings.Join(call.args, " ")
}
