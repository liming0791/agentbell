package adapter

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liming0791/agentbell/core/internal/hookaudit"
)

func TestCodexAuditHooksNormalizesConflictsAndExactReceiptOwnership(t *testing.T) {
	adapterValue := newTestCodexAdapter(t)
	legacyInvocation, err := adapterValue.hookInvocation()
	if err != nil {
		t.Fatal(err)
	}
	legacyCommand := legacyInvocation.shellCommand(false)
	legacyWindows := legacyInvocation.shellCommand(true)
	if err := adapterValue.writeReceipt(codexReceipt{
		Version:        1,
		Adapter:        codexAdapterID,
		HookPath:       adapterValue.hookPath(),
		Command:        legacyCommand,
		CommandWindows: legacyWindows,
	}); err != nil {
		t.Fatal(err)
	}

	adapterValue.BridgeExecutable = testBridgePath(t)
	adapterValue.ActiveGeneration = 42
	stableInvocation, err := adapterValue.hookInvocation()
	if err != nil {
		t.Fatal(err)
	}
	stableCommand := stableInvocation.shellCommand(false)
	stableWindows := stableInvocation.shellCommand(true)
	handler := func(command, windows string) map[string]any {
		return map[string]any{
			"type": "command", "command": command, "commandWindows": windows,
		}
	}
	root := map[string]any{
		"hooks": map[string]any{
			"Stop": []any{
				map[string]any{"hooks": []any{
					handler(stableCommand, stableWindows),
					handler(stableCommand, stableWindows),
					handler(legacyCommand, legacyWindows),
					handler("'/usr/local/bin/team-hook' notify", `"C:\Tools\team-hook.exe" notify`),
				}},
			},
		},
	}
	if err := writeJSONObject(adapterValue.hookPath(), root); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(adapterValue.hookPath())
	if err != nil {
		t.Fatal(err)
	}

	report, err := adapterValue.AuditHooks()
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.CurrentStableBridge != 1 ||
		report.Summary.OwnedDuplicate != 1 ||
		report.Summary.OwnedLegacy != 1 ||
		report.Summary.ExternalSameEvent != 1 {
		t.Fatalf("unexpected Codex report: %#v", report)
	}
	if len(report.Plan.Actions) != 2 {
		t.Fatalf("unexpected Codex plan: %#v", report.Plan)
	}
	for _, action := range report.Plan.Actions {
		if action.Operation != hookaudit.OperationRemove ||
			action.SourceFile != adapterValue.hookPath() ||
			len(action.Path) == 0 ||
			len(action.Args) == 0 {
			t.Fatalf("unsafe Codex action: %#v", action)
		}
	}
	after, err := os.ReadFile(adapterValue.hookPath())
	if err != nil {
		t.Fatal(err)
	}
	if !stringEqualBytes(before, after) {
		t.Fatal("Codex audit modified the Hook file")
	}
}

func TestClaudeAuditHooksUsesExecFormAndIgnoresInvalidReceipt(t *testing.T) {
	adapterValue := newTestClaudeAdapter(t)
	legacyInvocation, err := adapterValue.hookInvocation()
	if err != nil {
		t.Fatal(err)
	}
	if err := adapterValue.writeReceipt(claudeReceipt{
		Version:      1,
		Adapter:      claudeAdapterID,
		SettingsPath: filepath.Join(t.TempDir(), "different-settings.json"),
		Command:      legacyInvocation.Executable,
		Args:         legacyInvocation.Args,
	}); err != nil {
		t.Fatal(err)
	}
	adapterValue.BridgeExecutable = testBridgePath(t)
	adapterValue.ActiveGeneration = 7
	root := map[string]any{
		"hooks": map[string]any{
			"Stop": []any{
				map[string]any{"hooks": []any{
					claudeHandler(legacyInvocation.Executable, legacyInvocation.Args),
					claudeHandler("/usr/local/bin/team-hook", []string{"notify"}),
				}},
			},
		},
	}
	if err := writeJSONObject(adapterValue.settingsPath(), root); err != nil {
		t.Fatal(err)
	}

	report, err := adapterValue.AuditHooks()
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.OwnedLegacy != 0 ||
		report.Summary.ExternalSameEvent != 2 ||
		report.Summary.MissingStableBridge != len(claudeHookEvents) {
		t.Fatalf("invalid receipt granted ownership: %#v", report)
	}
	for _, action := range report.Plan.Actions {
		if action.Operation == hookaudit.OperationRemove {
			t.Fatalf("invalid receipt enabled removal: %#v", action)
		}
		if action.Form != hookaudit.FormExec {
			t.Fatalf("Claude plan lost exec form: %#v", action)
		}
	}
}

func TestClaudeAuditHooksExportsValidLegacyReceipt(t *testing.T) {
	adapterValue := newTestClaudeAdapter(t)
	legacyInvocation, err := adapterValue.hookInvocation()
	if err != nil {
		t.Fatal(err)
	}
	if err := adapterValue.writeReceipt(claudeReceipt{
		Version:      1,
		Adapter:      claudeAdapterID,
		SettingsPath: adapterValue.settingsPath(),
		Command:      legacyInvocation.Executable,
		Args:         legacyInvocation.Args,
	}); err != nil {
		t.Fatal(err)
	}
	adapterValue.BridgeExecutable = testBridgePath(t)
	adapterValue.ActiveGeneration = 8
	if err := writeJSONObject(adapterValue.settingsPath(), map[string]any{
		"hooks": map[string]any{
			"Stop": []any{map[string]any{
				"hooks": []any{
					claudeHandler(legacyInvocation.Executable, legacyInvocation.Args),
				},
			}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	report, err := adapterValue.AuditHooks()
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.OwnedLegacy != 1 {
		t.Fatalf("valid receipt was not exported: %#v", report)
	}
	foundRemove := false
	for _, action := range report.Plan.Actions {
		if action.Operation == hookaudit.OperationRemove {
			foundRemove = true
		}
	}
	if !foundRemove {
		t.Fatalf("legacy Claude Hook was not repairable: %#v", report.Plan)
	}
}

func TestClaudeAuditHooksAcceptsVersionCompatibleShellForm(t *testing.T) {
	adapterValue := newTestClaudeAdapter(t)
	adapterValue.BridgeExecutable = testBridgePath(t)
	adapterValue.ActiveGeneration = 9
	adapterValue.VersionOutput = func(string) (string, error) {
		return "2.0.19", nil
	}
	if _, err := adapterValue.Install(false); err != nil {
		t.Fatal(err)
	}
	report, err := adapterValue.AuditHooks()
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.CurrentStableBridge != 2 ||
		report.Summary.MissingStableBridge != 0 ||
		report.Summary.UnsafeStructure != 0 {
		t.Fatalf("compatible shell hooks were not current: %#v", report)
	}
	for _, finding := range report.Findings {
		if finding.Event == "StopFailure" ||
			finding.Event == "PermissionRequest" {
			t.Fatalf(
				"Claude 2.0.19 audit managed unsupported event: %#v",
				finding,
			)
		}
	}
}

func TestKimiAuditHooksUsesManagedRegionAsOwnershipProof(t *testing.T) {
	adapterValue := newTestKimiAdapter(t)
	legacyCommand, err := adapterValue.command()
	if err != nil {
		t.Fatal(err)
	}
	adapterValue.BridgeExecutable = testBridgePath(t)
	adapterValue.ActiveGeneration = 12
	external := "[[hooks]]\n" +
		"event = \"Stop\"\n" +
		"command = \"'/usr/local/bin/team-hook' notify\"\n\n"
	content := external + kimiRegionText(legacyCommand)
	if err := os.MkdirAll(filepath.Dir(adapterValue.configPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(adapterValue.configPath(), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	report, err := adapterValue.AuditHooks()
	if err != nil {
		t.Fatal(err)
	}
	if report.Summary.OwnedLegacy != len(kimiHookEvents) ||
		report.Summary.ExternalSameEvent != 1 ||
		report.Summary.MissingStableBridge != len(kimiHookEvents) {
		t.Fatalf("unexpected Kimi audit: %#v", report)
	}
	removeCount := 0
	for _, action := range report.Plan.Actions {
		if action.Operation == hookaudit.OperationRemove {
			removeCount++
			if action.SourceFile != adapterValue.configPath() ||
				len(action.Path) == 0 {
				t.Fatalf("Kimi action lost source locator: %#v", action)
			}
		}
	}
	if removeCount != len(kimiHookEvents) {
		t.Fatalf("managed region was not exactly repairable: %#v", report.Plan)
	}
}

func TestAdapterAuditHooksFailsClosedOnMissingOversizedAndUnsafeSources(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		adapterValue := newTestCodexAdapter(t)
		adapterValue.BridgeExecutable = testBridgePath(t)
		adapterValue.ActiveGeneration = 1
		if _, err := adapterValue.AuditHooks(); err == nil ||
			!errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing source did not fail closed: %v", err)
		}
	})
	t.Run("oversized", func(t *testing.T) {
		adapterValue := newTestClaudeAdapter(t)
		adapterValue.BridgeExecutable = testBridgePath(t)
		adapterValue.ActiveGeneration = 1
		if err := os.MkdirAll(filepath.Dir(adapterValue.settingsPath()), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			adapterValue.settingsPath(),
			[]byte(`{"padding":"`+strings.Repeat("x", maximumHookAuditBytes)+`"}`),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		if _, err := adapterValue.AuditHooks(); err == nil ||
			!strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("oversized source did not fail closed: %v", err)
		}
	})
	t.Run("unsafe-json-shape", func(t *testing.T) {
		adapterValue := newTestClaudeAdapter(t)
		adapterValue.BridgeExecutable = testBridgePath(t)
		adapterValue.ActiveGeneration = 1
		if err := writeJSONObject(adapterValue.settingsPath(), map[string]any{
			"hooks": []any{"not-an-object"},
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := adapterValue.AuditHooks(); err == nil {
			t.Fatal("unsafe JSON shape did not fail closed")
		}
	})
	t.Run("unsafe-handler", func(t *testing.T) {
		adapterValue := newTestClaudeAdapter(t)
		adapterValue.BridgeExecutable = testBridgePath(t)
		adapterValue.ActiveGeneration = 1
		if err := writeJSONObject(adapterValue.settingsPath(), map[string]any{
			"hooks": map[string]any{
				"Stop": []any{map[string]any{
					"hooks": []any{map[string]any{
						"type": "command", "command": "relative-hook", "args": []any{},
					}},
				}},
			},
		}); err != nil {
			t.Fatal(err)
		}
		report, err := adapterValue.AuditHooks()
		if err != nil {
			t.Fatal(err)
		}
		if !report.Plan.Blocked || report.Summary.UnsafeStructure != 1 {
			t.Fatalf("unsafe handler did not block reconcile: %#v", report)
		}
	})
	t.Run("malformed-kimi-region", func(t *testing.T) {
		adapterValue := newTestKimiAdapter(t)
		adapterValue.BridgeExecutable = testBridgePath(t)
		adapterValue.ActiveGeneration = 1
		if err := os.MkdirAll(filepath.Dir(adapterValue.configPath()), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			adapterValue.configPath(),
			[]byte(kimiRegionBeginPrefix+"broken\n[[hooks]]\n"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		if _, err := adapterValue.AuditHooks(); err == nil {
			t.Fatal("malformed Kimi region did not fail closed")
		}
	})
}

func TestAdapterAuditRequiresConfiguredStableBridge(t *testing.T) {
	adapterValue := newTestClaudeAdapter(t)
	if err := writeJSONObject(adapterValue.settingsPath(), map[string]any{"hooks": map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := adapterValue.AuditHooks(); err == nil ||
		!strings.Contains(err.Error(), "stable bridge") {
		t.Fatalf("legacy desired invocation was accepted: %v", err)
	}
}

func TestJSONHookAuditRejectsTopLevelAndNestedDuplicateKeys(t *testing.T) {
	tests := []struct {
		name     string
		path     func() string
		audit    func() error
		contents string
	}{
		{
			name: "codex top-level",
			path: func() string {
				return ""
			},
			contents: `{"hooks":{},"hooks":{}}`,
		},
		{
			name: "codex nested",
			path: func() string {
				return ""
			},
			contents: `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"one","command":"two","commandWindows":"three"}]}]}}`,
		},
		{
			name: "claude top-level",
			path: func() string {
				return ""
			},
			contents: `{"hooks":{},"hooks":{}}`,
		},
		{
			name: "claude nested",
			path: func() string {
				return ""
			},
			contents: `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"one","command":"two","args":[]}]}]}}`,
		},
	}

	for index := range tests {
		test := &tests[index]
		if strings.HasPrefix(test.name, "codex") {
			adapterValue := newTestCodexAdapter(t)
			adapterValue.BridgeExecutable = testBridgePath(t)
			adapterValue.ActiveGeneration = 1
			test.path = adapterValue.hookPath
			test.audit = func() error {
				_, err := adapterValue.AuditHooks()
				return err
			}
		} else {
			adapterValue := newTestClaudeAdapter(t)
			adapterValue.BridgeExecutable = testBridgePath(t)
			adapterValue.ActiveGeneration = 1
			test.path = adapterValue.settingsPath
			test.audit = func() error {
				_, err := adapterValue.AuditHooks()
				return err
			}
		}
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := test.path()
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(test.contents), 0o600); err != nil {
				t.Fatal(err)
			}
			err := test.audit()
			if err == nil || !strings.Contains(err.Error(), "duplicate JSON key") {
				t.Fatalf("duplicate key source was not rejected: %v", err)
			}
		})
	}
}

func TestDuplicateReceiptKeysNeverGrantHookOwnership(t *testing.T) {
	t.Run("codex", func(t *testing.T) {
		adapterValue := newTestCodexAdapter(t)
		legacy, err := adapterValue.hookInvocation()
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := json.Marshal(codexReceipt{
			Version:        1,
			Adapter:        codexAdapterID,
			HookPath:       adapterValue.hookPath(),
			Command:        legacy.shellCommand(false),
			CommandWindows: legacy.shellCommand(true),
		})
		if err != nil {
			t.Fatal(err)
		}
		receipt = append(
			[]byte(`{"adapter":"forged",`),
			receipt[1:]...,
		)
		if err := os.MkdirAll(filepath.Dir(adapterValue.receiptPath()), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(adapterValue.receiptPath(), receipt, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := writeJSONObject(adapterValue.hookPath(), map[string]any{
			"hooks": map[string]any{
				"Stop": []any{map[string]any{
					"hooks": []any{map[string]any{
						"type":           "command",
						"command":        legacy.shellCommand(false),
						"commandWindows": legacy.shellCommand(true),
					}},
				}},
			},
		}); err != nil {
			t.Fatal(err)
		}
		adapterValue.BridgeExecutable = testBridgePath(t)
		adapterValue.ActiveGeneration = 11
		report, err := adapterValue.AuditHooks()
		if err != nil {
			t.Fatal(err)
		}
		assertNoReceiptOwnership(t, report)
	})

	t.Run("claude", func(t *testing.T) {
		adapterValue := newTestClaudeAdapter(t)
		legacy, err := adapterValue.hookInvocation()
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := json.Marshal(claudeReceipt{
			Version:      1,
			Adapter:      claudeAdapterID,
			SettingsPath: adapterValue.settingsPath(),
			Command:      legacy.Executable,
			Args:         legacy.Args,
		})
		if err != nil {
			t.Fatal(err)
		}
		receipt = append(
			[]byte(`{"adapter":"forged",`),
			receipt[1:]...,
		)
		if err := os.MkdirAll(filepath.Dir(adapterValue.receiptPath()), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(adapterValue.receiptPath(), receipt, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := writeJSONObject(adapterValue.settingsPath(), map[string]any{
			"hooks": map[string]any{
				"Stop": []any{map[string]any{
					"hooks": []any{
						claudeHandler(legacy.Executable, legacy.Args),
					},
				}},
			},
		}); err != nil {
			t.Fatal(err)
		}
		adapterValue.BridgeExecutable = testBridgePath(t)
		adapterValue.ActiveGeneration = 12
		report, err := adapterValue.AuditHooks()
		if err != nil {
			t.Fatal(err)
		}
		assertNoReceiptOwnership(t, report)
	})
}

func TestUniqueJSONKeyValidationIsBoundedAndRecursive(t *testing.T) {
	deep := strings.Repeat(`{"child":`, maximumAuditJSONDepth+1) +
		`null` +
		strings.Repeat(`}`, maximumAuditJSONDepth+1)
	if err := validateUniqueJSONKeys(
		[]byte(deep),
		maximumHookAuditJSONTokens,
	); err == nil || !strings.Contains(err.Error(), "nesting") {
		t.Fatalf("deep JSON error = %v", err)
	}
	if err := validateUniqueJSONKeys(
		[]byte(`[0,1,2,3]`),
		3,
	); err == nil {
		t.Fatalf("token-bounded JSON error = %v", err)
	}
	if err := validateUniqueJSONKeys(
		[]byte(`[{"safe":1},{"nested":{"key":1,"key":2}}]`),
		maximumHookAuditJSONTokens,
	); err == nil || !strings.Contains(err.Error(), "duplicate JSON key") {
		t.Fatalf("recursive duplicate error = %v", err)
	}
}

func assertNoReceiptOwnership(t *testing.T, report hookaudit.Report) {
	t.Helper()
	if report.Summary.OwnedLegacy != 0 {
		t.Fatalf("duplicate receipt granted ownership: %#v", report)
	}
	for _, action := range report.Plan.Actions {
		if action.Operation == hookaudit.OperationRemove {
			t.Fatalf("duplicate receipt enabled removal: %#v", action)
		}
	}
}

func stringEqualBytes(left, right []byte) bool {
	var leftJSON any
	var rightJSON any
	if json.Unmarshal(left, &leftJSON) == nil && json.Unmarshal(right, &rightJSON) == nil {
		leftNormalized, _ := json.Marshal(leftJSON)
		rightNormalized, _ := json.Marshal(rightJSON)
		return string(leftNormalized) == string(rightNormalized)
	}
	return string(left) == string(right)
}
