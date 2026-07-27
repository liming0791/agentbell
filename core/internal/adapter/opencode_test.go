package adapter

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestOpenCodeAdapter(t *testing.T) *OpenCodeAdapter {
	t.Helper()
	root := t.TempDir()
	return &OpenCodeAdapter{
		Executable:   filepath.Join(root, "agentbell"),
		StateDir:     filepath.Join(root, "state"),
		OpenCodeHome: filepath.Join(root, ".config", "opencode"),
		Now: func() time.Time {
			return time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
		},
		LookPath: func(string) (string, error) {
			return filepath.Join(root, "opencode"), nil
		},
	}
}

func TestOpenCodeInstallVerifyUninstall(t *testing.T) {
	adapterValue := newTestOpenCodeAdapter(t)

	installed, err := adapterValue.Install(false)
	if err != nil {
		t.Fatal(err)
	}
	if !installed.Changed || !installed.Installed {
		t.Fatalf("unexpected install result: %#v", installed)
	}

	// Plugin file must exist and contain the marker.
	raw, err := os.ReadFile(adapterValue.pluginPath())
	if err != nil {
		t.Fatal(err)
	}
	if !isAgentBellPlugin(string(raw)) {
		t.Fatalf("plugin missing ownership marker: %s", raw)
	}
	if !strings.Contains(string(raw), adapterValue.Executable) {
		t.Fatalf("plugin missing executable path: %s", raw)
	}
	for _, event := range opencodeHookEvents {
		if !strings.Contains(string(raw), event) {
			t.Fatalf("plugin missing event %s: %s", event, raw)
		}
	}

	// Idempotent second install.
	second, err := adapterValue.Install(false)
	if err != nil || second.Changed {
		t.Fatalf("second install must be idempotent: %#v err=%v", second, err)
	}

	// Verify.
	verified, err := adapterValue.Verify()
	if err != nil || !verified.Installed {
		t.Fatalf("verify: %#v err=%v", verified, err)
	}

	// Uninstall.
	uninstalled, err := adapterValue.Uninstall(false)
	if err != nil {
		t.Fatal(err)
	}
	if !uninstalled.Changed || uninstalled.Backup == "" {
		t.Fatalf("unexpected uninstall result: %#v", uninstalled)
	}
	if _, err := os.Stat(adapterValue.pluginPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plugin file still exists after uninstall: %v", err)
	}
}

func TestOpenCodeDryRunDoesNotWrite(t *testing.T) {
	adapterValue := newTestOpenCodeAdapter(t)
	result, err := adapterValue.Install(true)
	if err != nil || !result.Changed {
		t.Fatalf("dry-run: %#v err=%v", result, err)
	}
	if _, err := os.Stat(adapterValue.pluginPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run wrote plugin: %v", err)
	}
}

func TestOpenCodePreservesOtherPlugins(t *testing.T) {
	adapterValue := newTestOpenCodeAdapter(t)
	pluginsDir := filepath.Dir(adapterValue.pluginPath())
	if err := os.MkdirAll(pluginsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	otherPlugin := filepath.Join(pluginsDir, "my-plugin.js")
	if err := os.WriteFile(otherPlugin, []byte("export const MyPlugin = async () => ({});\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := adapterValue.Install(false); err != nil {
		t.Fatal(err)
	}
	if _, err := adapterValue.Uninstall(false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(otherPlugin); err != nil {
		t.Fatalf("other plugin was removed: %v", err)
	}
}

func TestOpenCodeRejectsForeignAgentBellFile(t *testing.T) {
	adapterValue := newTestOpenCodeAdapter(t)
	pluginsDir := filepath.Dir(adapterValue.pluginPath())
	if err := os.MkdirAll(pluginsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// File named agentbell.js but without the ownership marker.
	if err := os.WriteFile(adapterValue.pluginPath(), []byte("export const Foo = async () => ({});\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := adapterValue.Install(false)
	if err == nil || !strings.Contains(err.Error(), "not owned by AgentBell") {
		t.Fatalf("expected ownership conflict error, got: %v", err)
	}
}

func TestOpenCodeUninstallNotInstalled(t *testing.T) {
	adapterValue := newTestOpenCodeAdapter(t)
	result, err := adapterValue.Uninstall(false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Changed {
		t.Fatalf("uninstall should not change when not installed: %#v", result)
	}
}

func TestOpenCodeDiagnoseWithoutRuntimeProof(t *testing.T) {
	adapterValue := newTestOpenCodeAdapter(t)
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

func TestOpenCodeDiagnoseWithRuntimeProof(t *testing.T) {
	adapterValue := newTestOpenCodeAdapter(t)
	if _, err := adapterValue.Install(false); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(adapterValue.pluginPath())
	if err != nil {
		t.Fatal(err)
	}
	seenAt := info.ModTime().Add(time.Second)
	if err := RecordRuntimeProof(adapterValue.StateDir, opencodeAdapterID, "task.completed", seenAt); err != nil {
		t.Fatal(err)
	}
	diagnosis := adapterValue.Diagnose()
	if !diagnosis.RuntimeVerified {
		t.Fatalf("diagnose should be verified with runtime proof: %#v", diagnosis)
	}
}

func TestOpenCodeVerifyUsesReceiptAfterExecutableMove(t *testing.T) {
	adapterValue := newTestOpenCodeAdapter(t)
	if _, err := adapterValue.Install(false); err != nil {
		t.Fatal(err)
	}
	// Simulate executable move.
	adapterValue.Executable = filepath.Join(t.TempDir(), "moved", "agentbell")
	verified, err := adapterValue.Verify()
	if err != nil || !verified.Installed {
		t.Fatalf("verify with receipt after executable move: %#v err=%v", verified, err)
	}
}

func TestOpenCodePluginTemplateContainsSpawn(t *testing.T) {
	content := opencodePluginTemplate("/usr/local/bin/agentbell")
	if !strings.Contains(content, "spawn") {
		t.Fatalf("plugin template must use child_process.spawn: %s", content)
	}
	if !strings.Contains(content, "--fail-open") {
		t.Fatalf("plugin template must pass --fail-open: %s", content)
	}
	if !strings.Contains(content, opencodePluginMarker) {
		t.Fatalf("plugin template must contain ownership marker: %s", content)
	}
}

func TestNewOpenCodeAdapterExplicitExecutable(t *testing.T) {
	root := t.TempDir()
	t.Setenv("OPENCODE_CONFIG_DIR", filepath.Join(root, "oc"))
	a, err := NewOpenCodeAdapter(filepath.Join(root, "bin", "agentbell"), filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	if a.OpenCodeHome != filepath.Join(root, "oc") {
		t.Fatalf("unexpected home: %s", a.OpenCodeHome)
	}
	if !filepath.IsAbs(a.Executable) {
		t.Fatalf("executable must be absolute: %s", a.Executable)
	}
}

func TestNewOpenCodeAdapterDefaultHome(t *testing.T) {
	t.Setenv("OPENCODE_CONFIG_DIR", "")
	a, err := NewOpenCodeAdapter("/usr/bin/agentbell", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(a.OpenCodeHome, filepath.Join(".config", "opencode")) {
		t.Fatalf("expected default home suffix .config/opencode, got: %s", a.OpenCodeHome)
	}
}

func TestOpenCodePlan(t *testing.T) {
	adapterValue := newTestOpenCodeAdapter(t)
	plan := adapterValue.Plan()
	if plan.Adapter != opencodeAdapterID {
		t.Fatalf("unexpected adapter: %s", plan.Adapter)
	}
	if !plan.Detected {
		t.Fatal("expected detected=true when lookPath succeeds")
	}
	if plan.HookPath != adapterValue.pluginPath() {
		t.Fatalf("unexpected hook path: %s", plan.HookPath)
	}
	if len(plan.Changes) == 0 {
		t.Fatal("expected non-empty changes")
	}
}

func TestOpenCodeDetectFallsBackToDirectory(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, ".config", "opencode")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	adapterValue := &OpenCodeAdapter{
		Executable:   filepath.Join(root, "agentbell"),
		StateDir:     filepath.Join(root, "state"),
		OpenCodeHome: home,
		LookPath: func(string) (string, error) {
			return "", errors.New("not found")
		},
	}
	if !adapterValue.Detect() {
		t.Fatal("expected detect=true when directory exists")
	}
	// Remove directory -> detect should be false.
	os.RemoveAll(home)
	if adapterValue.Detect() {
		t.Fatal("expected detect=false when neither binary nor directory exists")
	}
}
