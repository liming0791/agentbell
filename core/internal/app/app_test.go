package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/liming0791/agentbell/core/internal/config"
	"github.com/liming0791/agentbell/core/internal/service"
)

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("pipe closed")
}

type appServiceRunner struct {
	calls int
}

func (runner *appServiceRunner) Run(
	_ context.Context,
	_ string,
	_ ...string,
) ([]byte, error) {
	runner.calls++
	return []byte("ok"), nil
}

func TestVersionJSON(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run([]string{"version", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	var value map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if value["version"] == "" || value["platform"] == "" {
		t.Fatalf("incomplete version output: %#v", value)
	}
}

func TestEmitAndDeduplicate(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("AGENTBELL_STATE_DIR", stateDir)
	raw := `{"hook_event_name":"Stop","session_id":"s1","turn_id":"t1"}`
	for index := 0; index < 2; index++ {
		var stderr bytes.Buffer
		code := Run(
			[]string{"emit", "--adapter", "codex", "--surface", "cli", "--runtime", "host", "--stdin"},
			strings.NewReader(raw),
			&bytes.Buffer{},
			&stderr,
		)
		if code != 0 {
			t.Fatalf("code=%d stderr=%s", code, stderr.String())
		}
	}
	entries, err := os.ReadDir(filepath.Join(stateDir, "queue", "pending"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one pending event, got %d", len(entries))
	}
	proofPath := filepath.Join(stateDir, "adapters", "codex", "runtime-proof.json")
	proof, err := os.ReadFile(proofPath)
	if err != nil {
		t.Fatalf("runtime proof was not recorded: %v", err)
	}
	if !strings.Contains(string(proof), `"adapter": "codex"`) {
		t.Fatalf("unexpected runtime proof: %s", proof)
	}
	if !strings.Contains(string(proof), `"event": "task.completed"`) {
		t.Fatalf("runtime proof omitted event identity: %s", proof)
	}
}

func TestEmitSuppressesAmbiguousCodexApproval(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("AGENTBELL_STATE_DIR", stateDir)
	t.Setenv("AGENTBELL_DEBUG", "1")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(
		[]string{"emit", "--adapter", "codex", "--surface", "desktop", "--runtime", "host", "--stdin"},
		strings.NewReader(`{
			"hook_event_name":"PermissionRequest",
			"permission_mode":"default",
			"session_id":"session",
			"turn_id":"turn"
		}`),
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"suppressed": true`) {
		t.Fatalf("missing suppression result: %s", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(stateDir, "queue", "pending")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("suppressed approval created a queue: %v", err)
	}
	proof, err := os.ReadFile(filepath.Join(stateDir, "adapters", "codex", "runtime-proof.json"))
	if err != nil || !strings.Contains(string(proof), `"event": "approval.required"`) {
		t.Fatalf("suppressed Hook did not leave minimal runtime proof: %s err=%v", proof, err)
	}
}

func TestEmitInputLimitsAndFailOpen(t *testing.T) {
	var stderr bytes.Buffer
	code := Run(
		[]string{"emit", "--adapter", "codex", "--surface", "cli", "--runtime", "host", "--stdin"},
		strings.NewReader(strings.Repeat("x", maxInputSize+1)),
		&bytes.Buffer{},
		&stderr,
	)
	if code == 0 || !strings.Contains(stderr.String(), "exceeds") {
		t.Fatalf("expected size error: code=%d stderr=%s", code, stderr.String())
	}

	stderr.Reset()
	code = Run(
		[]string{"emit", "--adapter", "codex", "--surface", "cli", "--runtime", "host", "--stdin", "--fail-open"},
		strings.NewReader("{"),
		&bytes.Buffer{},
		&stderr,
	)
	if code != 0 {
		t.Fatalf("fail-open returned %d: %s", code, stderr.String())
	}
}

func TestReadLimitedBoundaries(t *testing.T) {
	if _, err := readLimited(strings.NewReader(" \n\t"), maxInputSize); err == nil {
		t.Fatal("empty input was accepted")
	}
	if _, err := readLimited(failingReader{}, maxInputSize); err == nil ||
		!strings.Contains(err.Error(), "pipe closed") {
		t.Fatalf("reader error was not returned: %v", err)
	}
	unicode := `{"message":"项目 🚀 𠮷"}`
	value, err := readLimited(strings.NewReader(unicode), maxInputSize)
	if err != nil || string(value) != unicode {
		t.Fatalf("unicode input: %q err=%v", value, err)
	}
	if _, err := readLimited(
		strings.NewReader(strings.Repeat("x", maxInputSize)),
		maxInputSize,
	); err != nil {
		t.Fatalf("exact-size input was rejected: %v", err)
	}
}

func TestHelpDoctorQueueAndUnsupported(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTBELL_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("AGENTBELL_CONFIG", filepath.Join(root, "missing-config.json"))
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := Run(nil, strings.NewReader(""), &stdout, &stderr); code != 0 ||
		!strings.Contains(stdout.String(), "Usage:") {
		t.Fatalf("help failed: %d %s", code, stderr.String())
	}
	stdout.Reset()
	if code := Run([]string{"doctor", "--json"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("doctor failed: %d %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"queue"`) {
		t.Fatalf("doctor output is incomplete: %s", stdout.String())
	}
	stdout.Reset()
	if code := Run(
		[]string{"queue", "list", "--state", "pending"},
		strings.NewReader(""),
		&stdout,
		&stderr,
	); code != 0 || strings.TrimSpace(stdout.String()) != "[]" {
		t.Fatalf("queue list failed: %d %s %s", code, stdout.String(), stderr.String())
	}
	stderr.Reset()
	if code := Run([]string{"unknown"}, strings.NewReader(""), &stdout, &stderr); code == 0 {
		t.Fatal("unsupported command succeeded")
	}
}

func TestSetupDryRunCommand(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTBELL_CONFIG", filepath.Join(root, "config.json"))
	t.Setenv("AGENTBELL_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("HOME", root)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(
		[]string{"setup", "--dry-run", "--json"},
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("setup --dry-run failed: %d %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "[dry-run]") {
		t.Fatalf("expected dry-run plan: %s", stdout.String())
	}
	var report map[string]any
	// The trailing JSON report must parse.
	start := strings.Index(stdout.String(), "\n{")
	if start < 0 {
		t.Fatalf("missing JSON report: %s", stdout.String())
	}
	if err := json.Unmarshal([]byte(stdout.String()[start+1:]), &report); err != nil {
		t.Fatalf("report is not valid JSON: %v", err)
	}
	if report["dryRun"] != true {
		t.Fatalf("unexpected report: %#v", report)
	}
	if _, err := os.Stat(filepath.Join(root, "config.json")); !os.IsNotExist(err) {
		t.Fatal("dry-run must not write the config")
	}
}

func writeFakeLarkCLI(t *testing.T, succeeds bool) string {
	t.Helper()
	directory := t.TempDir()
	if runtime.GOOS == "windows" {
		commandPath := filepath.Join(directory, "lark-cli.cmd")
		if err := os.WriteFile(commandPath, []byte("@echo off\r\nexit /b 0\r\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		powerShellPath := filepath.Join(directory, "lark-cli.ps1")
		script := "exit 0\n"
		if !succeeds {
			script = "Write-Error 'nope'\nexit 1\n"
		}
		if err := os.WriteFile(powerShellPath, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		return commandPath
	}
	path := filepath.Join(directory, "lark-cli")
	script := "#!/bin/sh\nexit 0\n"
	if !succeeds {
		script = "#!/bin/sh\necho nope >&2\nexit 1\n"
	}
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeTestConfig(t *testing.T, larkCLIPath string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	value := &config.Config{
		DefaultChannel: "feishu",
		LarkCLIPath:    larkCLIPath,
		Notifications: config.Notifications{
			Events:         []string{"task.completed"},
			IncludeSummary: false,
			PrivacyLevel:   "metadata-only",
		},
		Channels: []config.Channel{
			{ID: "feishu", Name: "通知群", Type: "feishu", ChatID: "oc_test", As: "bot"},
			{ID: "spare", Name: "备用群", Type: "feishu", ChatID: "oc_spare", As: "user"},
		},
	}
	if err := config.Save(path, value); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTBELL_CONFIG", path)
}

func TestTestCommandSendsMessage(t *testing.T) {
	writeTestConfig(t, writeFakeLarkCLI(t, true))
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run([]string{"test", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("test command failed: %d %s", code, stderr.String())
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["ok"] != true || result["channel"] != "feishu" || result["chatId"] != "oc_test" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestTestCommandChannelFlagAndFailure(t *testing.T) {
	writeTestConfig(t, writeFakeLarkCLI(t, false))
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(
		[]string{"test", "--channel", "spare", "--json"},
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if code == 0 {
		t.Fatal("expected send failure")
	}
	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["ok"] != false || result["channel"] != "spare" ||
		!strings.Contains(result["error"].(string), "lark-cli notification failed") {
		t.Fatalf("unexpected result: %#v", result)
	}

	stderr.Reset()
	code = Run([]string{"test", "--channel", "missing"}, strings.NewReader(""), &bytes.Buffer{}, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), `channel "missing" not found`) {
		t.Fatalf("expected unknown-channel error: %d %s", code, stderr.String())
	}
}

func TestTestCommandWithoutConfig(t *testing.T) {
	t.Setenv("AGENTBELL_CONFIG", filepath.Join(t.TempDir(), "missing.json"))
	var stderr bytes.Buffer
	code := Run([]string{"test"}, strings.NewReader(""), &bytes.Buffer{}, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), "agentbell setup") {
		t.Fatalf("expected setup guidance: %d %s", code, stderr.String())
	}
}

func TestTestCommandHumanOutput(t *testing.T) {
	writeTestConfig(t, writeFakeLarkCLI(t, true))
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run([]string{"test"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "测试消息已发送到频道 feishu") {
		t.Fatalf("unexpected output: %d %s %s", code, stdout.String(), stderr.String())
	}
}

func TestServiceInstallMigratesAbsoluteLarkPathOnMacOS(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("LaunchAgent service installation is macOS-specific")
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("AGENTBELL_CONFIG", filepath.Join(root, "config.json"))
	t.Setenv("AGENTBELL_LOG_DIR", filepath.Join(root, "logs"))

	larkDir := filepath.Join(root, "node", "bin")
	if err := os.MkdirAll(larkDir, 0o700); err != nil {
		t.Fatal(err)
	}
	larkPath := filepath.Join(larkDir, "lark-cli")
	if err := os.WriteFile(larkPath, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	launchctlDir := filepath.Join(root, "system-bin")
	if err := os.MkdirAll(launchctlDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(launchctlDir, "launchctl"),
		[]byte("#!/bin/sh\nexit 0\n"),
		0o755,
	); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", larkDir+string(os.PathListSeparator)+launchctlDir)

	value := `{
		"defaultChannel":"feishu",
		"notifications":{"events":["task.completed"],"privacyLevel":"metadata-only"},
		"channels":[{"id":"feishu","type":"feishu","chatId":"oc_test","as":"bot"}]
	}`
	if err := os.WriteFile(os.Getenv("AGENTBELL_CONFIG"), []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(
		[]string{"service", "install", "--json"},
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("service install failed: %d %s", code, stderr.String())
	}
	loaded, err := config.Load(os.Getenv("AGENTBELL_CONFIG"))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.LarkCLIPath != larkPath {
		t.Fatalf("lark-cli path was not migrated: %#v", loaded)
	}
	plistPath := filepath.Join(
		home,
		"Library",
		"LaunchAgents",
		"com.agentbell.service.plist",
	)
	plist, err := os.ReadFile(plistPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plist), larkDir) {
		t.Fatalf("LaunchAgent PATH does not include lark-cli runtime: %s", plist)
	}
}

func TestAdapterCommands(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTBELL_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("CODEX_HOME", filepath.Join(root, ".codex"))
	commands := [][]string{
		{"adapter", "plan", "codex"},
		{"adapter", "install", "codex", "--dry-run"},
		{"adapter", "install", "codex"},
		{"adapter", "verify", "codex"},
		{"adapter", "diagnose", "codex"},
		{"adapter", "uninstall", "codex"},
	}
	for _, arguments := range commands {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if code := Run(arguments, strings.NewReader(""), &stdout, &stderr); code != 0 {
			t.Fatalf("%v failed: %s", arguments, stderr.String())
		}
	}
}

func TestAdapterKimiCommands(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTBELL_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("KIMI_CODE_HOME", filepath.Join(root, ".kimi-code"))
	commands := [][]string{
		{"adapter", "plan", "kimi-code"},
		{"adapter", "install", "kimi-code", "--dry-run"},
		{"adapter", "install", "kimi-code"},
		{"adapter", "verify", "kimi-code"},
		{"adapter", "diagnose", "kimi-code"},
		{"adapter", "detect", "kimi-code"},
		{"adapter", "uninstall", "kimi-code"},
	}
	for _, arguments := range commands {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if code := Run(arguments, strings.NewReader(""), &stdout, &stderr); code != 0 {
			t.Fatalf("%v failed: %s", arguments, stderr.String())
		}
	}
	config, err := os.ReadFile(filepath.Join(root, ".kimi-code", "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(config), "--adapter kimi-code") {
		t.Fatalf("uninstall must remove the hooks region: %s", config)
	}
}

func TestAdapterClaudeCommandsAndUnifiedUninstall(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTBELL_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("CODEX_HOME", filepath.Join(root, ".codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, ".claude"))
	t.Setenv("KIMI_CODE_HOME", filepath.Join(root, ".kimi-code"))

	for _, id := range []string{"codex", "claude-code", "kimi-code"} {
		for _, arguments := range [][]string{
			{"adapter", "plan", id},
			{"adapter", "install", id},
			{"adapter", "verify", id},
			{"adapter", "diagnose", id},
		} {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if code := Run(arguments, strings.NewReader(""), &stdout, &stderr); code != 0 {
				t.Fatalf("%v failed: %s", arguments, stderr.String())
			}
		}
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := Run(
		[]string{"adapter", "uninstall", "all"},
		strings.NewReader(""),
		&stdout,
		&stderr,
	); code != 0 {
		t.Fatalf("unified uninstall failed: %s", stderr.String())
	}
	var results []map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &results); err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("unexpected uninstall results: %#v", results)
	}
	for _, path := range []string{
		filepath.Join(root, ".codex", "hooks.json"),
		filepath.Join(root, ".claude", "settings.json"),
		filepath.Join(root, ".kimi-code", "config.toml"),
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "--adapter") {
			t.Fatalf("unified uninstall left AgentBell hooks in %s: %s", path, raw)
		}
	}
}

func TestProductUninstallStopsServiceAndRemovesAllHooks(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTBELL_CONFIG", filepath.Join(root, "config.json"))
	t.Setenv("AGENTBELL_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("AGENTBELL_LOG_DIR", filepath.Join(root, "logs"))
	t.Setenv("CODEX_HOME", filepath.Join(root, ".codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, ".claude"))
	t.Setenv("KIMI_CODE_HOME", filepath.Join(root, ".kimi-code"))
	if err := os.WriteFile(os.Getenv("AGENTBELL_CONFIG"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, id := range supportedAdapterIDs {
		var stderr bytes.Buffer
		if code := Run(
			[]string{"adapter", "install", id},
			strings.NewReader(""),
			&bytes.Buffer{},
			&stderr,
		); code != 0 {
			t.Fatalf("install %s failed: %s", id, stderr.String())
		}
	}

	runner := &appServiceRunner{}
	manager := &service.Manager{
		GOOS:       "darwin",
		Executable: filepath.Join(root, "agentbell"),
		HomeDir:    filepath.Join(root, "home"),
		LogDir:     filepath.Join(root, "logs"),
		UID:        "501",
		Runner:     runner,
	}
	if _, err := manager.Install(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	originalNewServiceManager := newServiceManager
	newServiceManager = func(string, string) (*service.Manager, error) {
		return manager, nil
	}
	t.Cleanup(func() {
		newServiceManager = originalNewServiceManager
	})

	var dryRunJSON bytes.Buffer
	var stderr bytes.Buffer
	if code := Run(
		[]string{"uninstall", "--dry-run", "--json"},
		strings.NewReader(""),
		&dryRunJSON,
		&stderr,
	); code != 0 {
		t.Fatalf("product uninstall dry-run failed: %s", stderr.String())
	}
	var dryRun productUninstallReport
	if err := json.Unmarshal(dryRunJSON.Bytes(), &dryRun); err != nil {
		t.Fatal(err)
	}
	if !dryRun.DryRun || len(dryRun.Adapters) != len(supportedAdapterIDs) ||
		!dryRun.Service.Installed {
		t.Fatalf("unexpected product uninstall plan: %#v", dryRun)
	}
	if _, err := os.Stat(dryRun.Service.DefinitionPath); err != nil {
		t.Fatalf("dry-run removed service definition: %v", err)
	}

	var actualJSON bytes.Buffer
	stderr.Reset()
	if code := Run(
		[]string{"uninstall", "--json"},
		strings.NewReader(""),
		&actualJSON,
		&stderr,
	); code != 0 {
		t.Fatalf("product uninstall failed: %s", stderr.String())
	}
	var actual productUninstallReport
	if err := json.Unmarshal(actualJSON.Bytes(), &actual); err != nil {
		t.Fatal(err)
	}
	if actual.DryRun || len(actual.Adapters) != len(supportedAdapterIDs) ||
		actual.Service.Installed {
		t.Fatalf("unexpected product uninstall result: %#v", actual)
	}
	if _, err := os.Stat(dryRun.Service.DefinitionPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("product uninstall left service definition: %v", err)
	}
	for _, path := range []string{
		filepath.Join(root, ".codex", "hooks.json"),
		filepath.Join(root, ".claude", "settings.json"),
		filepath.Join(root, ".kimi-code", "config.toml"),
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "--adapter") {
			t.Fatalf("product uninstall left AgentBell hooks in %s: %s", path, raw)
		}
	}
	if _, err := os.Stat(os.Getenv("AGENTBELL_CONFIG")); err != nil {
		t.Fatalf("product uninstall removed preserved config: %v", err)
	}
}
