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

func newTestTraeAdapter(t *testing.T) *TraeAdapter {
	t.Helper()
	root := t.TempDir()
	return &TraeAdapter{
		Executable: filepath.Join(root, "AgentBell App", "agentbell"),
		StateDir:   filepath.Join(root, "state"),
		TraeHome:   filepath.Join(root, ".trae"),
		GOOS:       "darwin",
		Now: func() time.Time {
			return time.Date(2026, 7, 26, 13, 0, 0, 0, time.UTC)
		},
		LookPath: func(string) (string, error) {
			return "", errors.New("not found")
		},
	}
}

func TestTraeInstallVerifyUninstallPreservesHooks(t *testing.T) {
	adapterValue := newTestTraeAdapter(t)
	if err := os.MkdirAll(adapterValue.TraeHome, 0o700); err != nil {
		t.Fatal(err)
	}
	existing := `{
		"version":1,
		"hooks":{
			"Stop":[{"loop_limit":3,"hooks":[{"type":"command","command":"user-script"}]}]
		}
	}`
	if err := os.WriteFile(adapterValue.hookPath(), []byte(existing), 0o600); err != nil {
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
	if result, err := adapterValue.Verify(); err != nil || !result.Installed {
		t.Fatalf("verify: %#v err=%v", result, err)
	}
	raw, err := os.ReadFile(adapterValue.hookPath())
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
	if root["version"] != float64(1) || !strings.Contains(string(raw), "user-script") ||
		!hasShellHook(root, traeHookSpecs[0], command) {
		t.Fatalf("TRAE hooks were not merged safely: %s", raw)
	}

	uninstalled, err := adapterValue.Uninstall(false)
	if err != nil || !uninstalled.Changed || uninstalled.Backup == "" {
		t.Fatalf("uninstall: %#v err=%v", uninstalled, err)
	}
	afterRaw, err := os.ReadFile(adapterValue.hookPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(afterRaw), "user-script") ||
		strings.Contains(string(afterRaw), "--adapter trae") {
		t.Fatalf("uninstall damaged user hooks or left AgentBell: %s", afterRaw)
	}
}

func TestTraeDryRunVersionConflictAndMissingUninstall(t *testing.T) {
	adapterValue := newTestTraeAdapter(t)
	result, err := adapterValue.Install(true)
	if err != nil || !result.Changed {
		t.Fatalf("dry-run: %#v err=%v", result, err)
	}
	if _, err := os.Stat(adapterValue.hookPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run wrote hooks: %v", err)
	}
	result, err = adapterValue.Uninstall(false)
	if err != nil || result.Changed {
		t.Fatalf("missing uninstall: %#v err=%v", result, err)
	}
	if err := os.MkdirAll(adapterValue.TraeHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(adapterValue.hookPath(), []byte(`{"version":2,"hooks":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := adapterValue.Install(false); err == nil ||
		!strings.Contains(err.Error(), "version 1") {
		t.Fatalf("expected version conflict, got %v", err)
	}
	if err := os.WriteFile(adapterValue.hookPath(), []byte(`{"version":1,"hooks":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := adapterValue.Install(false); err == nil {
		t.Fatal("expected hook shape conflict")
	}
}

func TestTraeDiagnoseRequiresCompletionProof(t *testing.T) {
	adapterValue := newTestTraeAdapter(t)
	if _, err := adapterValue.Install(false); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(adapterValue.hookPath())
	if err != nil {
		t.Fatal(err)
	}
	seenAt := info.ModTime().Add(time.Second)
	if err := RecordRuntimeProof(
		adapterValue.StateDir,
		traeAdapterID,
		"approval.required",
		seenAt,
	); err != nil {
		t.Fatal(err)
	}
	if diagnosis := adapterValue.Diagnose(); diagnosis.RuntimeVerified {
		t.Fatalf("approval proof must not verify completion: %#v", diagnosis)
	}
	if err := RecordRuntimeProof(
		adapterValue.StateDir,
		traeAdapterID,
		"task.completed",
		seenAt,
	); err != nil {
		t.Fatal(err)
	}
	if diagnosis := adapterValue.Diagnose(); !diagnosis.RuntimeVerified || diagnosis.LastSeen == "" {
		t.Fatalf("completion proof was not accepted: %#v", diagnosis)
	}
	adapterValue.Executable = filepath.Join(t.TempDir(), "moved", "agentbell")
	if result, err := adapterValue.Verify(); err != nil || !result.Installed {
		t.Fatalf("receipt fallback failed: %#v err=%v", result, err)
	}
}

func TestTraeInstallMigratesOwnedExecutableCommand(t *testing.T) {
	adapterValue := newTestTraeAdapter(t)
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
	root, _, err := readJSONObject(adapterValue.hookPath())
	if err != nil {
		t.Fatal(err)
	}
	if hasShellHook(root, traeHookSpecs[0], oldCommand) ||
		!hasShellHook(root, traeHookSpecs[0], newCommand) {
		t.Fatalf("owned command was not migrated: %#v", root)
	}
	if result, err := adapterValue.Uninstall(false); err != nil || !result.Changed {
		t.Fatalf("migrated command was not uninstallable: %#v err=%v", result, err)
	}
}

func TestTraeInstallRemovesStaleManagedCommandWithoutReceipt(t *testing.T) {
	adapterValue := newTestTraeAdapter(t)
	staleCommand := shellQuote(filepath.Join(t.TempDir(), "old", "agentbell")) +
		" emit --adapter trae --surface ide --runtime host --stdin --fail-open"
	root := map[string]any{
		"version": float64(1),
		"hooks": map[string]any{
			"Notification": []any{
				map[string]any{
					"matcher": traeHookSpecs[0].Matcher,
					"hooks": []any{
						shellHookHandler(staleCommand),
						shellHookHandler("user-notifier"),
					},
				},
			},
		},
	}
	if err := os.MkdirAll(adapterValue.TraeHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONObject(adapterValue.hookPath(), root); err != nil {
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
	installed, _, err := readJSONObject(adapterValue.hookPath())
	if err != nil {
		t.Fatal(err)
	}
	if hasShellHook(installed, traeHookSpecs[0], staleCommand) ||
		!hasShellHook(installed, traeHookSpecs[0], currentCommand) ||
		!hasShellHook(installed, traeHookSpecs[0], "user-notifier") {
		t.Fatalf("unexpected migrated hooks: %#v", installed)
	}
}

func TestTraeRepairsOwnedMatcherAndRejectsMalformedGroups(t *testing.T) {
	adapterValue := newTestTraeAdapter(t)
	command, err := adapterValue.command()
	if err != nil {
		t.Fatal(err)
	}
	root := map[string]any{
		"version": float64(1),
		"hooks": map[string]any{
			"Notification": []any{
				map[string]any{
					"matcher": "idle_prompt",
					"hooks":   []any{shellHookHandler(command)},
				},
			},
		},
	}
	changed, err := mergeShellHooks(root, traeHookSpecs, command, true)
	if err != nil || !changed || !hasShellHook(root, traeHookSpecs[0], command) {
		t.Fatalf("owned matcher was not repaired: %#v err=%v", root, err)
	}
	malformed := map[string]any{
		"hooks": map[string]any{"Notification": []any{"invalid"}},
	}
	if _, err := removeShellHooks(malformed, traeHookSpecs, command); err == nil {
		t.Fatal("expected malformed matcher group error")
	}
}

func TestTraePlanDetectPlatformAndCommands(t *testing.T) {
	adapterValue := newTestTraeAdapter(t)
	if err := os.MkdirAll(adapterValue.TraeHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if !adapterValue.Detect() || !adapterValue.Plan().Detected {
		t.Fatal("TRAE config directory was not detected")
	}
	command, err := adapterValue.command()
	if err != nil || !strings.HasPrefix(command, "'") ||
		!strings.Contains(command, "--surface ide") {
		t.Fatalf("unexpected macOS command: %q err=%v", command, err)
	}
	adapterValue.GOOS = "windows"
	adapterValue.Executable = `C:\Program Files\AgentBell's\agentbell.exe`
	command, err = adapterValue.command()
	if err != nil || !strings.HasPrefix(command, "& '") ||
		!strings.Contains(command, "AgentBell''s") {
		t.Fatalf("unexpected PowerShell command: %q err=%v", command, err)
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

func TestNewTraeAdapterUsesGlobalHooksPath(t *testing.T) {
	configHome := filepath.Join(t.TempDir(), "trae-config")
	t.Setenv("TRAE_CONFIG_DIR", configHome)
	adapterValue, err := NewTraeAdapter("/usr/bin/agentbell", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if adapterValue.hookPath() != filepath.Join(configHome, "hooks.json") ||
		!filepath.IsAbs(adapterValue.Executable) {
		t.Fatalf("unexpected adapter paths: %#v", adapterValue)
	}
}

func TestTraeHomeSelectsCNProfile(t *testing.T) {
	userHome := filepath.Join(string(filepath.Separator), "Users", "agentbell")
	cnHome := filepath.Join(userHome, ".trae-cn")
	cnApp := filepath.Join(string(filepath.Separator), "Applications", "Trae CN.app")

	for _, test := range []struct {
		name   string
		goos   string
		exists map[string]bool
		want   string
	}{
		{name: "international default", goos: "darwin", want: filepath.Join(userHome, ".trae")},
		{name: "CN data root", goos: "windows", exists: map[string]bool{cnHome: true}, want: cnHome},
		{name: "CN macOS app", goos: "darwin", exists: map[string]bool{cnApp: true}, want: cnHome},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := traeHome(userHome, test.goos, func(path string) bool {
				return test.exists[path]
			})
			if got != test.want {
				t.Fatalf("traeHome() = %q, want %q", got, test.want)
			}
		})
	}
}
