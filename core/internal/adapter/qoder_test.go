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

func newTestQoderAdapter(t *testing.T) *QoderAdapter {
	t.Helper()
	root := t.TempDir()
	return &QoderAdapter{
		Executable: filepath.Join(root, "Program Files", "AgentBell", "agentbell.exe"),
		StateDir:   filepath.Join(root, "state"),
		QoderHome:  filepath.Join(root, ".qoder"),
		Now: func() time.Time {
			return time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
		},
		LookPath: func(string) (string, error) {
			return filepath.Join(root, "qoder.exe"), nil
		},
	}
}

func TestQoderInstallVerifyUninstallPreservesUnknownSettings(t *testing.T) {
	adapterValue := newTestQoderAdapter(t)
	if err := os.MkdirAll(adapterValue.QoderHome, 0o700); err != nil {
		t.Fatal(err)
	}
	existing := `{
		"model":"gpt-4",
		"permissions":{"allow":["Bash(npm test)"]},
		"hooks":{
			"Stop":[{"matcher":"","hooks":[{"type":"command","command":"user-script"}]}]
		}
	}`
	if err := os.WriteFile(adapterValue.settingsPath(), []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	installed, err := adapterValue.Install(false)
	if err != nil {
		t.Fatal(err)
	}
	if !installed.Changed || installed.Backup == "" || !installed.Installed {
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
	if root["model"] != "gpt-4" || !strings.Contains(string(raw), "Bash(npm test)") ||
		!strings.Contains(string(raw), "user-script") {
		t.Fatalf("unknown settings were not preserved: %s", raw)
	}
	command, args, err := adapterValue.command()
	if err != nil {
		t.Fatal(err)
	}
	for _, eventName := range qoderHookEvents {
		if !hasQoderHook(root, eventName, command, args) {
			t.Fatalf("missing %s hook: %s", eventName, raw)
		}
	}

	uninstalled, err := adapterValue.Uninstall(false)
	if err != nil {
		t.Fatal(err)
	}
	if !uninstalled.Changed {
		t.Fatalf("uninstall did not change: %#v", uninstalled)
	}
	afterRaw, err := os.ReadFile(adapterValue.settingsPath())
	if err != nil {
		t.Fatal(err)
	}
	var afterRoot map[string]any
	if err := json.Unmarshal(afterRaw, &afterRoot); err != nil {
		t.Fatal(err)
	}
	if afterRoot["model"] != "gpt-4" || !strings.Contains(string(afterRaw), "user-script") {
		t.Fatalf("user settings lost after uninstall: %s", afterRaw)
	}
	for _, eventName := range qoderHookEvents {
		if hasQoderHook(afterRoot, eventName, command, args) {
			t.Fatalf("hook %s survived uninstall: %s", eventName, afterRaw)
		}
	}
}

func TestQoderDryRunDoesNotWrite(t *testing.T) {
	adapterValue := newTestQoderAdapter(t)
	result, err := adapterValue.Install(true)
	if err != nil || !result.Changed {
		t.Fatalf("dry-run: %#v err=%v", result, err)
	}
	if _, err := os.Stat(adapterValue.settingsPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run wrote settings: %v", err)
	}
}

func TestQoderInstallRejectsShapeConflict(t *testing.T) {
	adapterValue := newTestQoderAdapter(t)
	if err := os.MkdirAll(adapterValue.QoderHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(adapterValue.settingsPath(), []byte(`{"hooks":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := adapterValue.Install(false); err == nil {
		t.Fatal("expected hook shape conflict")
	}
}

func TestQoderUninstallNotInstalled(t *testing.T) {
	adapterValue := newTestQoderAdapter(t)
	result, err := adapterValue.Uninstall(false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed {
		t.Fatalf("uninstall should not change when not installed: %#v", result)
	}
}

func TestQoderDiagnoseWithoutRuntimeProof(t *testing.T) {
	adapterValue := newTestQoderAdapter(t)
	if err := os.MkdirAll(adapterValue.QoderHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := adapterValue.Install(false); err != nil {
		t.Fatal(err)
	}
	diagnosis := adapterValue.Diagnose()
	if diagnosis.RuntimeVerified {
		t.Fatal("diagnose should not be verified without runtime proof")
	}
	if !strings.Contains(diagnosis.Message, "not yet observed") {
		t.Fatalf("unexpected diagnosis message: %s", diagnosis.Message)
	}
}

func TestQoderDiagnoseWithRuntimeProof(t *testing.T) {
	adapterValue := newTestQoderAdapter(t)
	if err := os.MkdirAll(adapterValue.QoderHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := adapterValue.Install(false); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(adapterValue.settingsPath())
	if err != nil {
		t.Fatal(err)
	}
	seenAt := info.ModTime().Add(time.Second)
	if err := RecordRuntimeProof(adapterValue.StateDir, qoderAdapterID, "task.completed", seenAt); err != nil {
		t.Fatal(err)
	}
	diagnosis := adapterValue.Diagnose()
	if !diagnosis.RuntimeVerified {
		t.Fatalf("diagnose should be verified with runtime proof: %#v", diagnosis)
	}
}

func TestQoderVerifyUsesReceiptAfterExecutableMove(t *testing.T) {
	adapterValue := newTestQoderAdapter(t)
	if err := os.MkdirAll(adapterValue.QoderHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := adapterValue.Install(false); err != nil {
		t.Fatal(err)
	}
	// Simulate executable move.
	adapterValue.Executable = filepath.Join(t.TempDir(), "moved", "agentbell.exe")
	verified, err := adapterValue.Verify()
	if err != nil || !verified.Installed {
		t.Fatalf("verify with receipt after executable move: %#v err=%v", verified, err)
	}
}

func TestNewQoderAdapterExplicitExecutable(t *testing.T) {
	root := t.TempDir()
	t.Setenv("QODER_CONFIG_DIR", filepath.Join(root, "qd"))
	a, err := NewQoderAdapter(filepath.Join(root, "bin", "agentbell"), filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	if a.QoderHome != filepath.Join(root, "qd") {
		t.Fatalf("unexpected home: %s", a.QoderHome)
	}
	if !filepath.IsAbs(a.Executable) {
		t.Fatalf("executable must be absolute: %s", a.Executable)
	}
}

func TestNewQoderAdapterDefaultHome(t *testing.T) {
	t.Setenv("QODER_CONFIG_DIR", "")
	a, err := NewQoderAdapter("/usr/bin/agentbell", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(a.QoderHome, ".qoder") {
		t.Fatalf("expected default home suffix .qoder, got: %s", a.QoderHome)
	}
}

func TestQoderPlan(t *testing.T) {
	adapterValue := newTestQoderAdapter(t)
	plan := adapterValue.Plan()
	if plan.Adapter != qoderAdapterID {
		t.Fatalf("unexpected adapter: %s", plan.Adapter)
	}
	if !plan.Detected {
		t.Fatal("expected detected=true when lookPath succeeds")
	}
	if plan.HookPath != adapterValue.settingsPath() {
		t.Fatalf("unexpected hook path: %s", plan.HookPath)
	}
	if len(plan.Changes) == 0 {
		t.Fatal("expected non-empty changes")
	}
}

func TestQoderDetectFallsBackToDirectory(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, ".qoder")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	adapterValue := &QoderAdapter{
		Executable: filepath.Join(root, "agentbell"),
		StateDir:   filepath.Join(root, "state"),
		QoderHome:  home,
		LookPath: func(string) (string, error) {
			return "", errors.New("not found")
		},
	}
	if !adapterValue.Detect() {
		t.Fatal("expected detect=true when directory exists")
	}
	os.RemoveAll(home)
	if adapterValue.Detect() {
		t.Fatal("expected detect=false when neither binary nor directory exists")
	}
}
