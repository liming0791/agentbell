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

func newTestCodexAdapter(t *testing.T) *CodexAdapter {
	t.Helper()
	root := t.TempDir()
	adapter := &CodexAdapter{
		Executable: filepath.Join(root, "Program Files", "AgentBell", "agentbell.exe"),
		StateDir:   filepath.Join(root, "state"),
		CodexHome:  filepath.Join(root, ".codex"),
		Now: func() time.Time {
			return time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
		},
		LookPath: func(string) (string, error) {
			return filepath.Join(root, "codex.exe"), nil
		},
	}
	return adapter
}

func TestCodexInstallVerifyUninstallPreservesUnknownHooks(t *testing.T) {
	adapter := newTestCodexAdapter(t)
	if err := os.MkdirAll(adapter.CodexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	existing := `{
		"custom":"preserved",
		"hooks":{
			"Stop":[{"matcher":"ignored","hooks":[{"type":"command","command":"user-script"}]}]
		}
	}`
	if err := os.WriteFile(adapter.hookPath(), []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	installed, err := adapter.Install(false)
	if err != nil {
		t.Fatal(err)
	}
	if !installed.Changed || installed.Backup == "" {
		t.Fatalf("install did not change or backup: %#v", installed)
	}
	if !strings.Contains(installed.Message, "/hooks") {
		t.Fatalf("install omitted the Codex trust reminder: %#v", installed)
	}
	if _, err := adapter.Install(false); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Verify(); err != nil {
		t.Fatal(err)
	}

	removed, err := adapter.Uninstall(false)
	if err != nil {
		t.Fatal(err)
	}
	if !removed.Changed || removed.Backup == "" {
		t.Fatalf("uninstall did not change or backup: %#v", removed)
	}
	value, err := os.ReadFile(adapter.hookPath())
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(value, &root); err != nil {
		t.Fatal(err)
	}
	if root["custom"] != "preserved" || !strings.Contains(string(value), "user-script") {
		t.Fatalf("user hook was not preserved: %s", value)
	}
	if strings.Contains(string(value), "AgentBell") || strings.Contains(string(value), "--adapter codex") {
		t.Fatalf("AgentBell hook was not removed: %s", value)
	}
}

func TestCodexDiagnoseRequiresRuntimeProofAfterHookChange(t *testing.T) {
	adapterValue := newTestCodexAdapter(t)
	if _, err := adapterValue.Install(false); err != nil {
		t.Fatal(err)
	}
	before := adapterValue.Diagnose()
	if !before.Installed || before.RuntimeVerified ||
		!strings.Contains(before.Message, "/hooks") {
		t.Fatalf("unexpected pre-runtime diagnosis: %#v", before)
	}
	info, err := os.Stat(adapterValue.hookPath())
	if err != nil {
		t.Fatal(err)
	}
	seenAt := info.ModTime().Add(time.Second)
	if err := RecordRuntimeProof(
		adapterValue.StateDir,
		codexAdapterID,
		"approval.required",
		seenAt,
	); err != nil {
		t.Fatal(err)
	}
	approvalOnly := adapterValue.Diagnose()
	if approvalOnly.RuntimeVerified {
		t.Fatalf("approval proof must not verify the Stop hook: %#v", approvalOnly)
	}
	if err := RecordRuntimeProof(
		adapterValue.StateDir,
		codexAdapterID,
		"task.completed",
		seenAt,
	); err != nil {
		t.Fatal(err)
	}
	after := adapterValue.Diagnose()
	if !after.RuntimeVerified || after.LastSeen == "" {
		t.Fatalf("runtime proof was not reflected: %#v", after)
	}
}

func TestCodexInstallRemovesOnlyLegacyAgentBellPermissionHook(t *testing.T) {
	adapterValue := newTestCodexAdapter(t)
	command, commandWindows, err := adapterValue.commands()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(adapterValue.CodexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	existing := map[string]any{
		"hooks": map[string]any{
			"PermissionRequest": []any{
				map[string]any{
					"hooks": []any{
						map[string]any{
							"type":           "command",
							"command":        command,
							"commandWindows": commandWindows,
						},
					},
				},
				map[string]any{
					"hooks": []any{
						map[string]any{
							"type":    "command",
							"command": "user-permission-handler",
						},
					},
				},
			},
		},
	}
	if err := writeJSONObject(adapterValue.hookPath(), existing); err != nil {
		t.Fatal(err)
	}
	if _, err := adapterValue.Install(false); err != nil {
		t.Fatal(err)
	}
	value, err := os.ReadFile(adapterValue.hookPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(value), "user-permission-handler") {
		t.Fatalf("legacy cleanup removed the wrong handler: %s", value)
	}
	if !strings.Contains(string(value), `"Stop"`) {
		t.Fatalf("completion hook was not installed: %s", value)
	}
}

func TestCodexInstallOmitsAndHealsLegacyDescription(t *testing.T) {
	// 新安装不得写入顶层 description：Codex 严格解析 hooks.json，
	// 未知顶层字段会导致整个文件被忽略。
	fresh := newTestCodexAdapter(t)
	if _, err := fresh.Install(false); err != nil {
		t.Fatal(err)
	}
	value, err := os.ReadFile(fresh.hookPath())
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(value, &root); err != nil {
		t.Fatal(err)
	}
	if _, ok := root["description"]; ok {
		t.Fatalf("install must not write a top-level description: %s", value)
	}

	// 早期版本写入的 description 在重复安装时被清除（自愈）。
	legacy := newTestCodexAdapter(t)
	if err := os.MkdirAll(legacy.CodexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	broken := `{
		"description":"Lifecycle hooks including AgentBell notifications.",
		"hooks":{}
	}`
	if err := os.WriteFile(legacy.hookPath(), []byte(broken), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Install(false); err != nil {
		t.Fatal(err)
	}
	healed, err := os.ReadFile(legacy.hookPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(healed), "description") {
		t.Fatalf("legacy description was not removed: %s", healed)
	}
	if _, err := legacy.Verify(); err != nil {
		t.Fatal(err)
	}
}

func TestCodexDryRunDoesNotWrite(t *testing.T) {
	adapter := newTestCodexAdapter(t)
	result, err := adapter.Install(true)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Changed {
		t.Fatal("expected dry-run change")
	}
	if _, err := os.Stat(adapter.hookPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run wrote hooks: %v", err)
	}
}

func TestCodexInstallRejectsConflictingShape(t *testing.T) {
	adapter := newTestCodexAdapter(t)
	if err := os.MkdirAll(adapter.CodexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(adapter.hookPath(), []byte(`{"hooks":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Install(false); err == nil {
		t.Fatal("expected hook shape conflict")
	}
}

func TestCodexPlanDiagnoseAndConstructor(t *testing.T) {
	t.Setenv("CODEX_HOME", filepath.Join(t.TempDir(), ".codex"))
	adapter, err := NewCodexAdapter("relative-agentbell", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(adapter.Executable) {
		t.Fatalf("executable is not absolute: %s", adapter.Executable)
	}
	adapter.LookPath = func(string) (string, error) {
		return "", errors.New("missing")
	}
	plan := adapter.Plan()
	if plan.Adapter != "codex" || plan.Detected || len(plan.Changes) != 3 {
		t.Fatalf("unexpected plan: %#v", plan)
	}
	diagnose := adapter.Diagnose()
	if diagnose.Installed || diagnose.Message == "" {
		t.Fatalf("unexpected diagnosis: %#v", diagnose)
	}
	if err := os.MkdirAll(adapter.CodexHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if !adapter.Detect() {
		t.Fatal("Codex App/shared config directory should count as detected")
	}
	result, err := adapter.Uninstall(true)
	if err != nil || result.Changed || result.Message == "" {
		t.Fatalf("unexpected empty uninstall: %#v %v", result, err)
	}
}

func TestCodexRejectsUnsafeExecutablePath(t *testing.T) {
	adapter := newTestCodexAdapter(t)
	adapter.Executable = "bad\npath"
	if _, err := adapter.Install(true); err == nil {
		t.Fatal("expected unsafe executable error")
	}
}

func TestCodexAdapterConformanceFixtures(t *testing.T) {
	fixtures := []struct {
		name       string
		executable string
	}{
		{
			name:       "windows",
			executable: `C:\Program Files\AgentBell\agentbell.exe`,
		},
		{
			name:       "macos",
			executable: "/Applications/AgentBell/agentbell",
		},
		{
			name:       "linux",
			executable: "/opt/agentbell/bin/agentbell",
		},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			adapterValue := newTestCodexAdapter(t)
			adapterValue.Executable = fixture.executable

			plan := adapterValue.Plan()
			if plan.Executable != fixture.executable || !plan.Detected {
				t.Fatalf("unexpected plan: %#v", plan)
			}
			firstInstall, err := adapterValue.Install(false)
			if err != nil || !firstInstall.Changed {
				t.Fatalf("first install: %#v err=%v", firstInstall, err)
			}
			secondInstall, err := adapterValue.Install(false)
			if err != nil || secondInstall.Changed {
				t.Fatalf("second install was not idempotent: %#v err=%v", secondInstall, err)
			}
			verified, err := adapterValue.Verify()
			if err != nil || !verified.Installed {
				t.Fatalf("verify: %#v err=%v", verified, err)
			}
			hooks, err := os.ReadFile(adapterValue.hookPath())
			if err != nil {
				t.Fatal(err)
			}
			escapedExecutable := strings.ReplaceAll(fixture.executable, `\`, `\\`)
			if !strings.Contains(string(hooks), fixture.executable) &&
				!strings.Contains(string(hooks), escapedExecutable) {
				t.Fatalf("hook does not use the Core path: %s", hooks)
			}
			firstUninstall, err := adapterValue.Uninstall(false)
			if err != nil || !firstUninstall.Changed {
				t.Fatalf("first uninstall: %#v err=%v", firstUninstall, err)
			}
			secondUninstall, err := adapterValue.Uninstall(false)
			if err != nil || secondUninstall.Changed {
				t.Fatalf(
					"second uninstall was not idempotent: %#v err=%v",
					secondUninstall,
					err,
				)
			}
			diagnosis := adapterValue.Diagnose()
			if diagnosis.Installed || diagnosis.Message == "" {
				t.Fatalf("unexpected diagnosis: %#v", diagnosis)
			}
		})
	}
}
