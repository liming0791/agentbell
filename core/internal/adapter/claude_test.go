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

func newTestClaudeAdapter(t *testing.T) *ClaudeAdapter {
	t.Helper()
	root := t.TempDir()
	return &ClaudeAdapter{
		Executable: filepath.Join(root, "Program Files", "AgentBell", "agentbell.exe"),
		StateDir:   filepath.Join(root, "state"),
		ClaudeHome: filepath.Join(root, ".claude"),
		Now: func() time.Time {
			return time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)
		},
		LookPath: func(string) (string, error) {
			return filepath.Join(root, "claude.exe"), nil
		},
	}
}

func TestClaudeInstallVerifyUninstallPreservesUnknownSettings(t *testing.T) {
	adapterValue := newTestClaudeAdapter(t)
	if err := os.MkdirAll(adapterValue.ClaudeHome, 0o700); err != nil {
		t.Fatal(err)
	}
	existing := `{
		"model":"sonnet",
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
	if root["model"] != "sonnet" || !strings.Contains(string(raw), "Bash(npm test)") ||
		!strings.Contains(string(raw), "user-script") {
		t.Fatalf("unknown settings were not preserved: %s", raw)
	}
	command, args, err := adapterValue.command()
	if err != nil {
		t.Fatal(err)
	}
	for _, eventName := range claudeHookEvents {
		if !hasClaudeHook(root, eventName, command, args) {
			t.Fatalf("missing %s hook: %s", eventName, raw)
		}
	}
	if !strings.Contains(string(raw), `"args"`) ||
		!strings.Contains(string(raw), `"--fail-open"`) {
		t.Fatalf("Claude hooks must use fail-open exec form: %s", raw)
	}

	plannedRemoval, err := adapterValue.Uninstall(true)
	if err != nil || !plannedRemoval.Changed ||
		!strings.Contains(plannedRemoval.Message, "would be uninstalled") {
		t.Fatalf("unexpected uninstall plan: %#v err=%v", plannedRemoval, err)
	}
	removed, err := adapterValue.Uninstall(false)
	if err != nil {
		t.Fatal(err)
	}
	if !removed.Changed || removed.Backup == "" {
		t.Fatalf("unexpected uninstall result: %#v", removed)
	}
	restored, err := os.ReadFile(adapterValue.settingsPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(restored), "user-script") ||
		!strings.Contains(string(restored), "Bash(npm test)") ||
		strings.Contains(string(restored), "--adapter") {
		t.Fatalf("uninstall did not preserve user settings: %s", restored)
	}
}

func TestClaudeNotificationMatcherAndRuntimeProof(t *testing.T) {
	adapterValue := newTestClaudeAdapter(t)
	if _, err := adapterValue.Install(false); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(adapterValue.settingsPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "idle_prompt|agent_needs_input") {
		t.Fatalf("notification hook should avoid unrelated notifications: %s", raw)
	}

	before := adapterValue.Diagnose()
	if !before.Installed || before.RuntimeVerified {
		t.Fatalf("unexpected pre-runtime diagnosis: %#v", before)
	}
	info, err := os.Stat(adapterValue.settingsPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordRuntimeProof(
		adapterValue.StateDir,
		claudeAdapterID,
		"task.completed",
		info.ModTime().Add(time.Second),
	); err != nil {
		t.Fatal(err)
	}
	after := adapterValue.Diagnose()
	if !after.RuntimeVerified || after.LastSeen == "" {
		t.Fatalf("runtime proof was not reflected: %#v", after)
	}
}

func TestClaudeDryRunAndMalformedSettings(t *testing.T) {
	adapterValue := newTestClaudeAdapter(t)
	result, err := adapterValue.Install(true)
	if err != nil || !result.Changed {
		t.Fatalf("dry-run: %#v err=%v", result, err)
	}
	if _, err := os.Stat(adapterValue.settingsPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dry-run wrote settings: %v", err)
	}

	if err := os.MkdirAll(adapterValue.ClaudeHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(adapterValue.settingsPath(), []byte(`{"hooks":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := adapterValue.Install(false); err == nil {
		t.Fatal("expected hook shape conflict")
	}
}

func TestClaudeConstructorDetectAndUnsafeExecutable(t *testing.T) {
	root := t.TempDir()
	claudeHome := filepath.Join(root, "custom-claude")
	t.Setenv("CLAUDE_CONFIG_DIR", claudeHome)
	adapterValue, err := NewClaudeAdapter("relative-agentbell", filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(adapterValue.Executable) || adapterValue.ClaudeHome != claudeHome {
		t.Fatalf("unexpected adapter: %#v", adapterValue)
	}
	adapterValue.LookPath = func(string) (string, error) {
		return "", errors.New("missing")
	}
	if adapterValue.Detect() {
		t.Fatal("adapter should not detect an absent CLI or config directory")
	}
	if err := os.MkdirAll(claudeHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if !adapterValue.Detect() {
		t.Fatal("Desktop/shared config directory should count as detected")
	}
	adapterValue.Executable = "bad\npath"
	if _, err := adapterValue.Install(true); err == nil {
		t.Fatal("expected unsafe executable error")
	}
}

func TestClaudeAdapterConformanceFixtures(t *testing.T) {
	fixtures := []string{
		`C:\Program Files\AgentBell\agentbell.exe`,
		"/Applications/AgentBell/agentbell",
		"/opt/agentbell/bin/agentbell",
	}
	for _, executable := range fixtures {
		t.Run(executable, func(t *testing.T) {
			adapterValue := newTestClaudeAdapter(t)
			adapterValue.Executable = executable
			first, err := adapterValue.Install(false)
			if err != nil || !first.Changed {
				t.Fatalf("install: %#v err=%v", first, err)
			}
			second, err := adapterValue.Install(false)
			if err != nil || second.Changed {
				t.Fatalf("idempotent install: %#v err=%v", second, err)
			}
			if _, err := adapterValue.Verify(); err != nil {
				t.Fatal(err)
			}
			firstRemoval, err := adapterValue.Uninstall(false)
			if err != nil || !firstRemoval.Changed {
				t.Fatalf("uninstall: %#v err=%v", firstRemoval, err)
			}
			secondRemoval, err := adapterValue.Uninstall(false)
			if err != nil || secondRemoval.Changed {
				t.Fatalf("idempotent uninstall: %#v err=%v", secondRemoval, err)
			}
		})
	}
}
