package adapter

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestMigrationFixtureAdapterReceiptsV1(t *testing.T) {
	t.Run("codex", testMigrationFixtureCodexReceiptV1)
	t.Run("claude", testMigrationFixtureClaudeReceiptV1)
	t.Run("kimi", testMigrationFixtureKimiReceiptV1)
	t.Run("opencode", testMigrationFixtureOpenCodeReceiptV1)
	t.Run("qoder", testMigrationFixtureQoderReceiptV1)
}

func testMigrationFixtureCodexReceiptV1(t *testing.T) {
	adapterValue := newTestCodexAdapter(t)
	var legacy codexReceipt
	installAdapterReceiptFixture(t, adapterValue.receiptPath(), "codex-v1.json", &legacy)

	oldHandler := map[string]any{
		"type":           "command",
		"command":        legacy.Command,
		"commandWindows": legacy.CommandWindows,
		"timeout":        float64(5),
	}
	root := map[string]any{
		"fixture": "preserved",
		"hooks": map[string]any{
			"Stop": []any{map[string]any{
				"hooks": []any{
					oldHandler,
					map[string]any{"type": "command", "command": "fixture-user-hook"},
				},
			}},
		},
	}
	writeAdapterFixtureJSON(t, adapterValue.hookPath(), root)

	adapterValue.BridgeExecutable = testBridgePath(t)
	adapterValue.ActiveGeneration = 101
	result, err := adapterValue.Install(false)
	if err != nil || !result.Changed {
		t.Fatalf("migrate Codex receipt fixture: %#v, %v", result, err)
	}
	raw := readAdapterFixtureFile(t, adapterValue.hookPath())
	assertExactAdapterMigration(t, raw, legacy.Command, adapterValue.BridgeExecutable)
	if !bytes.Contains(raw, []byte("fixture-user-hook")) {
		t.Fatal("Codex migration removed an external Hook")
	}
	receipt, err := adapterValue.readReceipt()
	if err != nil {
		t.Fatal(err)
	}
	expectedCommand, expectedWindows, err := adapterValue.commands()
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Version != bridgeReceiptVersion ||
		receipt.BridgeProtocol != stableBridgeProtocol ||
		receipt.ActivationGeneration != adapterValue.ActiveGeneration ||
		receipt.Command != expectedCommand ||
		receipt.CommandWindows != expectedWindows ||
		receipt.HookPath != adapterValue.hookPath() {
		t.Fatalf("unexpected migrated Codex receipt: %#v", receipt)
	}
}

func testMigrationFixtureClaudeReceiptV1(t *testing.T) {
	adapterValue := newTestClaudeAdapter(t)
	var legacy claudeReceipt
	installAdapterReceiptFixture(t, adapterValue.receiptPath(), "claude-v1.json", &legacy)

	hooks := make(map[string]any, len(claudeHookEvents))
	for _, eventName := range claudeHookEvents {
		handlers := []any{claudeHandler(legacy.Command, legacy.Args)}
		if eventName == "Stop" {
			handlers = append(
				handlers,
				map[string]any{"type": "command", "command": "fixture-user-hook"},
			)
		}
		hooks[eventName] = []any{map[string]any{"hooks": handlers}}
	}
	writeAdapterFixtureJSON(t, adapterValue.settingsPath(), map[string]any{
		"fixture": "preserved",
		"hooks":   hooks,
	})

	adapterValue.BridgeExecutable = testBridgePath(t)
	adapterValue.ActiveGeneration = 102
	result, err := adapterValue.Install(false)
	if err != nil || !result.Changed {
		t.Fatalf("migrate Claude receipt fixture: %#v, %v", result, err)
	}
	raw := readAdapterFixtureFile(t, adapterValue.settingsPath())
	assertExactAdapterMigration(t, raw, legacy.Command, adapterValue.BridgeExecutable)
	if !bytes.Contains(raw, []byte("fixture-user-hook")) {
		t.Fatal("Claude migration removed an external Hook")
	}
	receipt, err := adapterValue.readReceipt()
	if err != nil {
		t.Fatal(err)
	}
	expectedCommand, expectedArgs, err := adapterValue.command()
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Version != bridgeReceiptVersion ||
		receipt.BridgeProtocol != stableBridgeProtocol ||
		receipt.ActivationGeneration != adapterValue.ActiveGeneration ||
		receipt.Command != expectedCommand ||
		!equalStrings(receipt.Args, expectedArgs) ||
		receipt.SettingsPath != adapterValue.settingsPath() {
		t.Fatalf("unexpected migrated Claude receipt: %#v", receipt)
	}
}

func testMigrationFixtureKimiReceiptV1(t *testing.T) {
	adapterValue := newTestKimiAdapter(t)
	var legacy kimiReceipt
	installAdapterReceiptFixture(t, adapterValue.receiptPath(), "kimi-v1.json", &legacy)
	if err := os.MkdirAll(filepath.Dir(adapterValue.configPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	config := "fixture_user_setting = true\n\n" + kimiRegionText(legacy.Command)
	if err := os.WriteFile(adapterValue.configPath(), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	adapterValue.BridgeExecutable = testBridgePath(t)
	adapterValue.ActiveGeneration = 103
	result, err := adapterValue.Install(false)
	if err != nil || !result.Changed {
		t.Fatalf("migrate Kimi receipt fixture: %#v, %v", result, err)
	}
	raw := readAdapterFixtureFile(t, adapterValue.configPath())
	assertExactAdapterMigration(t, raw, legacy.Command, adapterValue.BridgeExecutable)
	if !bytes.Contains(raw, []byte("fixture_user_setting = true")) {
		t.Fatal("Kimi migration removed external TOML")
	}
	receipt, err := adapterValue.readReceipt()
	if err != nil {
		t.Fatal(err)
	}
	expectedCommand, err := adapterValue.command()
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Version != bridgeReceiptVersion ||
		receipt.BridgeProtocol != stableBridgeProtocol ||
		receipt.ActivationGeneration != adapterValue.ActiveGeneration ||
		receipt.Command != expectedCommand ||
		receipt.HookPath != adapterValue.configPath() {
		t.Fatalf("unexpected migrated Kimi receipt: %#v", receipt)
	}
}

func testMigrationFixtureOpenCodeReceiptV1(t *testing.T) {
	adapterValue := newTestOpenCodeAdapter(t)
	var legacy opencodeReceipt
	installAdapterReceiptFixture(t, adapterValue.receiptPath(), "opencode-v1.json", &legacy)

	currentExecutable := adapterValue.Executable
	adapterValue.Executable = legacy.Executable
	legacyPlugin, err := adapterValue.pluginContent()
	if err != nil {
		t.Fatal(err)
	}
	adapterValue.Executable = currentExecutable
	if err := os.MkdirAll(filepath.Dir(adapterValue.pluginPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(adapterValue.pluginPath(), []byte(legacyPlugin), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := adapterValue.Install(false)
	if err != nil || !result.Changed {
		t.Fatalf("migrate OpenCode receipt fixture: %#v, %v", result, err)
	}
	raw := readAdapterFixtureFile(t, adapterValue.pluginPath())
	expected, err := adapterValue.pluginContent()
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != expected || bytes.Contains(raw, []byte(legacy.Executable)) {
		t.Fatalf("OpenCode migration was not exact: %s", raw)
	}
	receipt, err := adapterValue.readReceipt()
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Version != 1 ||
		receipt.Executable != adapterValue.Executable ||
		receipt.PluginPath != adapterValue.pluginPath() {
		t.Fatalf("unexpected migrated OpenCode receipt: %#v", receipt)
	}
	assertAdapterMigrationPrivacy(t, raw)
}

func testMigrationFixtureQoderReceiptV1(t *testing.T) {
	adapterValue := newTestQoderAdapter(t)
	var legacy qoderReceipt
	installAdapterReceiptFixture(t, adapterValue.receiptPath(), "qoder-v1.json", &legacy)

	hooks := make(map[string]any, len(qoderHookEvents))
	for _, eventName := range qoderHookEvents {
		handlers := []any{qoderHandler(legacy.Command, legacy.Args)}
		if eventName == "Stop" {
			handlers = append(
				handlers,
				map[string]any{"type": "command", "command": "fixture-user-hook"},
			)
		}
		hooks[eventName] = []any{map[string]any{"hooks": handlers}}
	}
	writeAdapterFixtureJSON(t, adapterValue.settingsPath(), map[string]any{
		"fixture": "preserved",
		"hooks":   hooks,
	})

	result, err := adapterValue.Install(false)
	if err != nil || !result.Changed {
		t.Fatalf("migrate Qoder receipt fixture: %#v, %v", result, err)
	}
	raw := readAdapterFixtureFile(t, adapterValue.settingsPath())
	assertExactAdapterMigration(t, raw, legacy.Command, adapterValue.Executable)
	if !bytes.Contains(raw, []byte("fixture-user-hook")) {
		t.Fatal("Qoder migration removed an external Hook")
	}
	receipt, err := adapterValue.readReceipt()
	if err != nil {
		t.Fatal(err)
	}
	expectedCommand, expectedArgs, err := adapterValue.command()
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Version != 1 ||
		receipt.Command != expectedCommand ||
		!equalStrings(receipt.Args, expectedArgs) ||
		receipt.SettingsPath != adapterValue.settingsPath() {
		t.Fatalf("unexpected migrated Qoder receipt: %#v", receipt)
	}
}

func installAdapterReceiptFixture(
	t *testing.T,
	destination,
	name string,
	target any,
) {
	t.Helper()
	raw := readAdapterMigrationFixture(t, "receipts", name)
	assertAdapterMigrationPrivacy(t, raw)
	lower := strings.ToLower(string(raw))
	for _, hostPath := range []string{"/users/", "\\users\\"} {
		if strings.Contains(lower, hostPath) {
			t.Fatalf("migration fixture contains a host user path %q", hostPath)
		}
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readAdapterMigrationFixture(t *testing.T, elements ...string) []byte {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve migration fixture source")
	}
	pathElements := []string{
		filepath.Dir(source),
		"..",
		"..",
		"testdata",
		"migrations",
	}
	pathElements = append(pathElements, elements...)
	raw, err := os.ReadFile(filepath.Join(pathElements...))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func writeAdapterFixtureJSON(t *testing.T, path string, value map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeJSONObject(path, value); err != nil {
		t.Fatal(err)
	}
}

func readAdapterFixtureFile(t *testing.T, path string) []byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	assertAdapterMigrationPrivacy(t, raw)
	return raw
}

func assertExactAdapterMigration(
	t *testing.T,
	raw []byte,
	legacyCommand,
	currentExecutable string,
) {
	t.Helper()
	if bytes.Contains(raw, []byte(legacyCommand)) {
		t.Fatalf("legacy AgentBell command survived migration: %s", raw)
	}
	if !bytes.Contains(raw, []byte(encodedConfigPath(currentExecutable))) {
		t.Fatalf("current AgentBell executable missing after migration: %s", raw)
	}
	assertAdapterMigrationPrivacy(t, raw)
}

func assertAdapterMigrationPrivacy(t *testing.T, raw []byte) {
	t.Helper()
	lower := strings.ToLower(string(raw))
	for _, forbidden := range []string{
		`"cwd"`,
		`"summary"`,
		`"prompt"`,
		`"sessionid"`,
		`"taskid"`,
		`"turnid"`,
		`"token"`,
		`"secret"`,
	} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("migration fixture/output contains private field or host path %q", forbidden)
		}
	}
}
