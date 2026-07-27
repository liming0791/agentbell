package adapter

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestQoderWorkAdapter(t *testing.T) *QoderWorkAdapter {
	t.Helper()
	root := t.TempDir()
	return &QoderWorkAdapter{
		Executable:    filepath.Join(root, "AgentBell App", "agentbell"),
		StateDir:      filepath.Join(root, "state"),
		QoderWorkHome: filepath.Join(root, ".qoderwork"),
		GOOS:          "darwin",
		Now: func() time.Time {
			return time.Date(2026, 7, 26, 13, 0, 0, 0, time.UTC)
		},
		LookPath: func(string) (string, error) {
			return "", errors.New("not found")
		},
	}
}

func TestQoderWorkInstallVerifyUninstallPreservesSettings(t *testing.T) {
	adapterValue := newTestQoderWorkAdapter(t)
	if err := os.MkdirAll(adapterValue.QoderWorkHome, 0o700); err != nil {
		t.Fatal(err)
	}
	existing := `{
		"theme":"dark",
		"hooks":{
			"Stop":[{"hooks":[{"type":"command","command":"user-script"}]}]
		}
	}`
	if err := os.WriteFile(adapterValue.settingsPath(), []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	installed, err := adapterValue.Install(false)
	if err != nil {
		t.Fatal(err)
	}
	if !installed.Changed || !installed.Installed || installed.Backup == "" {
		t.Fatalf("unexpected install result: %#v", installed)
	}
	second, err := adapterValue.Install(false)
	if err != nil || second.Changed {
		t.Fatalf("second install must be idempotent: %#v err=%v", second, err)
	}
	verified, err := adapterValue.Verify()
	if err != nil || !verified.Installed {
		t.Fatalf("verify: %#v err=%v", verified, err)
	}
	raw, err := os.ReadFile(adapterValue.settingsPath())
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	command, err := adapterValue.command()
	if err != nil {
		t.Fatal(err)
	}
	if root["theme"] != "dark" || !strings.Contains(string(raw), "user-script") {
		t.Fatalf("user settings were not preserved: %s", raw)
	}
	for _, spec := range qoderWorkHookSpecs {
		if !hasShellHook(root, spec, command) {
			t.Fatalf("missing %s hook: %s", spec.Event, raw)
		}
	}

	uninstalled, err := adapterValue.Uninstall(false)
	if err != nil || !uninstalled.Changed || uninstalled.Backup == "" {
		t.Fatalf("uninstall: %#v err=%v", uninstalled, err)
	}
	afterRaw, err := os.ReadFile(adapterValue.settingsPath())
	if err != nil {
		t.Fatal(err)
	}
	var after map[string]any
	if err := json.Unmarshal(afterRaw, &after); err != nil {
		t.Fatal(err)
	}
	if after["theme"] != "dark" || !strings.Contains(string(afterRaw), "user-script") {
		t.Fatalf("user settings were lost: %s", afterRaw)
	}
	for _, spec := range qoderWorkHookSpecs {
		if hasShellHook(after, spec, command) {
			t.Fatalf("AgentBell hook survived uninstall: %s", afterRaw)
		}
	}
}

func TestQoderWorkDryRunConflictAndMissingUninstall(t *testing.T) {
	adapterValue := newTestQoderWorkAdapter(t)
	result, err := adapterValue.Install(true)
	if err != nil || !result.Changed {
		t.Fatalf("dry-run: %#v err=%v", result, err)
	}
	if _, err := os.Stat(adapterValue.settingsPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run wrote settings: %v", err)
	}
	result, err = adapterValue.Uninstall(false)
	if err != nil || result.Changed {
		t.Fatalf("missing uninstall: %#v err=%v", result, err)
	}
	if err := os.MkdirAll(adapterValue.QoderWorkHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(adapterValue.settingsPath(), []byte(`{"hooks":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := adapterValue.Install(false); err == nil {
		t.Fatal("expected hook shape conflict")
	}
}

func TestQoderWorkDiagnoseAndReceiptFallback(t *testing.T) {
	adapterValue := newTestQoderWorkAdapter(t)
	if _, err := adapterValue.Install(false); err != nil {
		t.Fatal(err)
	}
	if diagnosis := adapterValue.Diagnose(); diagnosis.RuntimeVerified ||
		!strings.Contains(diagnosis.Message, "restart QoderWork") {
		t.Fatalf("unexpected pre-proof diagnosis: %#v", diagnosis)
	}
	info, err := os.Stat(adapterValue.settingsPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordRuntimeProof(
		adapterValue.StateDir,
		qoderWorkAdapterID,
		"task.completed",
		info.ModTime().Add(time.Second),
	); err != nil {
		t.Fatal(err)
	}
	if diagnosis := adapterValue.Diagnose(); !diagnosis.RuntimeVerified || diagnosis.LastSeen == "" {
		t.Fatalf("fresh proof was not accepted: %#v", diagnosis)
	}
	adapterValue.Executable = filepath.Join(t.TempDir(), "moved", "agentbell")
	if result, err := adapterValue.Verify(); err != nil || !result.Installed {
		t.Fatalf("receipt fallback failed: %#v err=%v", result, err)
	}
}

func TestQoderWorkInstallMigratesOwnedExecutableCommand(t *testing.T) {
	adapterValue := newTestQoderWorkAdapter(t)
	if _, err := adapterValue.Install(false); err != nil {
		t.Fatal(err)
	}
	oldCommand, err := adapterValue.command()
	if err != nil {
		t.Fatal(err)
	}
	adapterValue.Executable = filepath.Join(t.TempDir(), "next version", "agentbell")
	newCommand, err := adapterValue.command()
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapterValue.Install(false)
	if err != nil || !result.Changed {
		t.Fatalf("command migration failed: %#v err=%v", result, err)
	}
	root, _, err := readJSONObject(adapterValue.settingsPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range qoderWorkHookSpecs {
		if hasShellHook(root, spec, oldCommand) || !hasShellHook(root, spec, newCommand) {
			t.Fatalf("owned command was not migrated for %s: %#v", spec.Event, root)
		}
	}
	if result, err := adapterValue.Uninstall(false); err != nil || !result.Changed {
		t.Fatalf("migrated command was not uninstallable: %#v err=%v", result, err)
	}
}

func TestQoderWorkInstallRemovesStaleManagedCommandWithoutReceipt(t *testing.T) {
	adapterValue := newTestQoderWorkAdapter(t)
	staleCommand := shellQuote(filepath.Join(t.TempDir(), "old", "agentbell")) +
		" emit --adapter qoder-work --surface desktop --runtime host --stdin --fail-open"
	userCommand := "notify-user --adapter qoder-work"
	root := map[string]any{"hooks": map[string]any{}}
	for _, spec := range qoderWorkHookSpecs {
		hooks := root["hooks"].(map[string]any)
		hooks[spec.Event] = []any{
			map[string]any{
				"hooks": []any{
					shellHookHandler(staleCommand),
					shellHookHandler(userCommand),
				},
			},
		}
	}
	if err := os.MkdirAll(adapterValue.QoderWorkHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONObject(adapterValue.settingsPath(), root); err != nil {
		t.Fatal(err)
	}
	result, err := adapterValue.Install(false)
	if err != nil || !result.Changed {
		t.Fatalf("stale command migration failed: %#v err=%v", result, err)
	}
	currentCommand, err := adapterValue.command()
	if err != nil {
		t.Fatal(err)
	}
	installed, _, err := readJSONObject(adapterValue.settingsPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range qoderWorkHookSpecs {
		if hasShellHook(installed, spec, staleCommand) ||
			!hasShellHook(installed, spec, currentCommand) ||
			!hasShellHook(installed, spec, userCommand) {
			t.Fatalf("unexpected migrated hooks for %s: %#v", spec.Event, installed)
		}
	}
}

func TestQoderWorkPlanDetectAndPlatformCommands(t *testing.T) {
	adapterValue := newTestQoderWorkAdapter(t)
	if err := os.MkdirAll(adapterValue.QoderWorkHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if !adapterValue.Detect() || !adapterValue.Plan().Detected {
		t.Fatal("QoderWork config directory was not detected")
	}
	command, err := adapterValue.command()
	if err != nil || !strings.HasPrefix(command, "'") ||
		!strings.Contains(command, "--surface desktop") {
		t.Fatalf("unexpected macOS command: %q err=%v", command, err)
	}
	adapterValue.GOOS = "windows"
	adapterValue.Executable = `C:\Program Files\AgentBell\agentbell.exe`
	command, err = adapterValue.command()
	if err != nil || !strings.HasPrefix(command, `"C:\Program Files`) {
		t.Fatalf("unexpected Windows command: %q err=%v", command, err)
	}
	adapterValue.GOOS = "linux"
	if _, err := adapterValue.Install(false); err == nil {
		t.Fatal("unsupported install succeeded")
	}
	if result, err := adapterValue.Uninstall(false); err != nil ||
		!strings.Contains(result.Message, "not supported") {
		t.Fatalf("unsupported uninstall must be a no-op: %#v err=%v", result, err)
	}
}

func TestNewQoderWorkAdapterUsesIndependentHome(t *testing.T) {
	configHome := filepath.Join(t.TempDir(), "qoder-work-config")
	t.Setenv("QODERWORK_CONFIG_DIR", configHome)
	adapterValue, err := NewQoderWorkAdapter("/usr/bin/agentbell", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if adapterValue.QoderWorkHome != configHome ||
		!filepath.IsAbs(adapterValue.Executable) {
		t.Fatalf("unexpected adapter paths: %#v", adapterValue)
	}
}

func TestQoderWorkHomeSelectsCNProfile(t *testing.T) {
	userHome := filepath.Join(string(filepath.Separator), "Users", "agentbell")
	cnHome := filepath.Join(userHome, ".qoderworkcn")
	cnApp := filepath.Join(string(filepath.Separator), "Applications", "QoderWork CN.app")

	for _, test := range []struct {
		name   string
		goos   string
		exists map[string]bool
		want   string
	}{
		{name: "international default", goos: "darwin", want: filepath.Join(userHome, ".qoderwork")},
		{name: "CN data root", goos: "windows", exists: map[string]bool{cnHome: true}, want: cnHome},
		{name: "CN macOS app", goos: "darwin", exists: map[string]bool{cnApp: true}, want: cnHome},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := qoderWorkHome(userHome, test.goos, func(path string) bool {
				return test.exists[path]
			})
			if got != test.want {
				t.Fatalf("qoderWorkHome() = %q, want %q", got, test.want)
			}
		})
	}
}
