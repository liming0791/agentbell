package adapter

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
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
		GOOS: "darwin",
		VersionOutput: func(string) (string, error) {
			return "2.1.139 (Claude Code)", nil
		},
	}
}

func TestClaudeHookVersionSelectsCompatibleHandlerForm(t *testing.T) {
	tests := []struct {
		name       string
		output     string
		versionErr error
		wantExec   bool
	}{
		{name: "last legacy patch", output: "2.1.138 (Claude Code)"},
		{name: "threshold", output: "2.1.139 (Claude Code)", wantExec: true},
		{name: "new major", output: "3.0.0", wantExec: true},
		{name: "threshold prerelease", output: "2.1.139-beta.1"},
		{name: "unparseable", output: "Claude Code development build"},
		{name: "probe error", versionErr: errors.New("version unavailable")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapterValue := newTestClaudeAdapter(t)
			adapterValue.BridgeExecutable = testBridgePath(t)
			adapterValue.ActiveGeneration = 7
			adapterValue.VersionOutput = func(string) (string, error) {
				return test.output, test.versionErr
			}
			command, args, err := adapterValue.command()
			if err != nil {
				t.Fatal(err)
			}
			if test.wantExec {
				if command != adapterValue.BridgeExecutable ||
					len(args) == 0 ||
					args[0] != "hook-v1" {
					t.Fatalf("exec handler = %q %#v", command, args)
				}
				return
			}
			if len(args) != 0 ||
				!strings.Contains(command, " hook-v1 --adapter claude-code ") {
				t.Fatalf("legacy handler = %q %#v", command, args)
			}
		})
	}
}

func TestClaudeHookEventsAreVersionNegotiated(t *testing.T) {
	tests := []struct {
		name   string
		output string
		err    error
		want   []string
	}{
		{
			name:   "2.0.19 baseline",
			output: "2.0.19 (Claude Code)",
			want:   []string{"Stop", "Notification"},
		},
		{
			name:   "PermissionRequest prerelease excluded",
			output: "2.0.45-beta.1",
			want:   []string{"Stop", "Notification"},
		},
		{
			name:   "PermissionRequest threshold",
			output: "2.0.45",
			want:   []string{"Stop", "Notification", "PermissionRequest"},
		},
		{
			name:   "StopFailure prerelease excluded",
			output: "2.1.78-rc.1",
			want:   []string{"Stop", "Notification", "PermissionRequest"},
		},
		{
			name:   "StopFailure threshold",
			output: "2.1.78",
			want:   claudeHookEvents,
		},
		{
			name:   "exec form threshold retains all events",
			output: "2.1.139",
			want:   claudeHookEvents,
		},
		{
			name: "unknown version is conservative",
			err:  errors.New("version unavailable"),
			want: []string{"Stop", "Notification"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapterValue := newTestClaudeAdapter(t)
			adapterValue.VersionOutput = func(string) (string, error) {
				return test.output, test.err
			}
			selected, err := adapterValue.commandDetails()
			if err != nil {
				t.Fatal(err)
			}
			if !equalStrings(selected.Events, test.want) {
				t.Fatalf("events = %#v, want %#v", selected.Events, test.want)
			}
		})
	}
}

func TestClaudeLegacyShellCommandIsSafelyConstructed(t *testing.T) {
	t.Run("posix", func(t *testing.T) {
		adapterValue := newTestClaudeAdapter(t)
		adapterValue.GOOS = "linux"
		adapterValue.BridgeExecutable = filepath.Join(
			t.TempDir(),
			"Agent Bell's Data",
			"agentbell-bridge",
		)
		adapterValue.ActiveGeneration = 3
		adapterValue.VersionOutput = func(string) (string, error) {
			return "2.0.19", nil
		}
		command, args, err := adapterValue.command()
		if err != nil {
			t.Fatal(err)
		}
		if len(args) != 0 {
			t.Fatalf("legacy POSIX handler retained args: %#v", args)
		}
		parsed, err := parseAuditShellInvocation(command)
		if err != nil {
			t.Fatalf("parse legacy POSIX command %q: %v", command, err)
		}
		if parsed.Executable != adapterValue.BridgeExecutable ||
			len(parsed.Args) == 0 ||
			parsed.Args[0] != "hook-v1" {
			t.Fatalf("legacy POSIX command changed argv: %#v", parsed)
		}
	})

	t.Run("windows", func(t *testing.T) {
		adapterValue := newTestClaudeAdapter(t)
		adapterValue.GOOS = "windows"
		adapterValue.Executable =
			`C:\Program Files\AgentBell\agentbell-bridge.exe`
		adapterValue.BridgeExecutable = ""
		adapterValue.VersionOutput = func(string) (string, error) {
			return "2.0.19", nil
		}
		command, args, err := adapterValue.command()
		if err != nil {
			t.Fatal(err)
		}
		if len(args) != 0 ||
			!strings.HasPrefix(
				command,
				`"C:\Program Files\AgentBell\agentbell-bridge.exe" emit `,
			) {
			t.Fatalf("legacy Windows handler = %q %#v", command, args)
		}
	})
}

func TestClaudeLegacyShellCommandRejectsUnsafeWindowsPaths(t *testing.T) {
	for _, executable := range []string{
		`C:\Users\%USERNAME%\agentbell.exe`,
		`C:\AgentBell\bad!path.exe`,
		`C:\AgentBell\bad&path.exe`,
		`C:\AgentBell\$bad.exe`,
		"C:\\AgentBell\\bad`path.exe",
		`C:\AgentBell\bad;path.exe`,
		"C:\\AgentBell\\bad\npath.exe",
		`C:\AgentBell\"bad.exe`,
	} {
		t.Run(executable, func(t *testing.T) {
			adapterValue := newTestClaudeAdapter(t)
			adapterValue.GOOS = "windows"
			adapterValue.BridgeExecutable = ""
			adapterValue.Executable = executable
			adapterValue.VersionOutput = func(string) (string, error) {
				return "2.0.19", nil
			}
			if _, _, err := adapterValue.command(); err == nil {
				t.Fatalf("unsafe legacy path was accepted: %q", executable)
			}
		})
	}
}

func TestClaudeLegacyShellCommandRejectsUnsafePOSIXPathsAndPlatforms(t *testing.T) {
	for _, executable := range []string{
		"/opt/AgentBell/bad\npath",
		"/opt/AgentBell/bad\rpath",
		"/opt/AgentBell/bad\x00path",
	} {
		t.Run(executable, func(t *testing.T) {
			adapterValue := newTestClaudeAdapter(t)
			adapterValue.GOOS = "linux"
			adapterValue.Executable = executable
			adapterValue.VersionOutput = func(string) (string, error) {
				return "2.0.19", nil
			}
			if _, _, err := adapterValue.command(); err == nil {
				t.Fatalf("unsafe legacy POSIX path was accepted: %q", executable)
			}
		})
	}
	adapterValue := newTestClaudeAdapter(t)
	adapterValue.GOOS = "plan9"
	adapterValue.VersionOutput = func(string) (string, error) {
		return "2.0.19", nil
	}
	if _, _, err := adapterValue.command(); err == nil ||
		!strings.Contains(err.Error(), "not supported") {
		t.Fatalf("unsupported platform error = %v", err)
	}
}

func TestClaudeInstallMigratesExecAndLegacyHandlersIdempotently(t *testing.T) {
	adapterValue := newTestClaudeAdapter(t)
	adapterValue.BridgeExecutable = testBridgePath(t)
	adapterValue.ActiveGeneration = 9
	if _, err := adapterValue.Install(false); err != nil {
		t.Fatal(err)
	}
	if value, err := os.ReadFile(adapterValue.settingsPath()); err != nil ||
		!strings.Contains(string(value), `"args"`) {
		t.Fatalf("initial exec handler missing: %s err=%v", value, err)
	}

	adapterValue.VersionOutput = func(string) (string, error) {
		return "2.0.19", nil
	}
	if verified, err := adapterValue.Verify(); err == nil ||
		verified.Installed ||
		!strings.Contains(strings.ToLower(err.Error()), "incompatible") {
		t.Fatalf("old Claude accepted exec handler: %#v err=%v", verified, err)
	}
	legacy, err := adapterValue.Install(false)
	if err != nil || !legacy.Changed {
		t.Fatalf("exec-to-legacy migration: %#v err=%v", legacy, err)
	}
	legacyBytes, err := os.ReadFile(adapterValue.settingsPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(legacyBytes), `"args"`) {
		t.Fatalf("exec handler survived legacy migration: %s", legacyBytes)
	}
	receipt, err := adapterValue.readReceipt()
	if err != nil || receipt.HookForm != claudeHookFormShell {
		t.Fatalf("legacy receipt = %#v err=%v", receipt, err)
	}
	if second, err := adapterValue.Install(false); err != nil || second.Changed {
		t.Fatalf("legacy reinstall is not idempotent: %#v err=%v", second, err)
	}

	adapterValue.VersionOutput = func(string) (string, error) {
		return "2.1.139", nil
	}
	execResult, err := adapterValue.Install(false)
	if err != nil || !execResult.Changed {
		t.Fatalf("legacy-to-exec migration: %#v err=%v", execResult, err)
	}
	execBytes, err := os.ReadFile(adapterValue.settingsPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(execBytes), `"args"`) {
		t.Fatalf("exec handler was not restored: %s", execBytes)
	}
	if second, err := adapterValue.Install(false); err != nil || second.Changed {
		t.Fatalf("exec reinstall is not idempotent: %#v err=%v", second, err)
	}
}

func TestClaudeInstallMigratesVersionedEventsAndPreservesExternalHooks(t *testing.T) {
	adapterValue := newTestClaudeAdapter(t)
	adapterValue.BridgeExecutable = testBridgePath(t)
	adapterValue.ActiveGeneration = 11
	if _, err := adapterValue.Install(false); err != nil {
		t.Fatal(err)
	}
	root, _, err := readJSONObject(adapterValue.settingsPath())
	if err != nil {
		t.Fatal(err)
	}
	hooks, err := objectField(root, "hooks", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, eventName := range []string{"StopFailure", "PermissionRequest"} {
		groups, err := arrayField(hooks, eventName, false)
		if err != nil {
			t.Fatal(err)
		}
		hooks[eventName] = append(groups, map[string]any{
			"hooks": []any{map[string]any{
				"type":    "command",
				"command": "external-" + eventName,
			}},
		})
	}
	if err := writeJSONObject(adapterValue.settingsPath(), root); err != nil {
		t.Fatal(err)
	}

	assertVersion := func(
		version string,
		wantEvents []string,
		wantChanged bool,
	) {
		t.Helper()
		adapterValue.VersionOutput = func(string) (string, error) {
			return version, nil
		}
		result, err := adapterValue.Install(false)
		if err != nil || result.Changed != wantChanged {
			t.Fatalf(
				"install %s: %#v err=%v, want changed=%v",
				version,
				result,
				err,
				wantChanged,
			)
		}
		selected, err := adapterValue.commandDetails()
		if err != nil {
			t.Fatal(err)
		}
		installedRoot, _, err := readJSONObject(adapterValue.settingsPath())
		if err != nil {
			t.Fatal(err)
		}
		for _, eventName := range claudeHookEvents {
			wantAgentBell := slices.Contains(wantEvents, eventName)
			if got := hasClaudeHook(
				installedRoot,
				eventName,
				selected.Command,
				selected.Args,
			); got != wantAgentBell {
				t.Fatalf(
					"%s AgentBell hook at %s = %v, want %v",
					eventName,
					version,
					got,
					wantAgentBell,
				)
			}
		}
		raw, err := os.ReadFile(adapterValue.settingsPath())
		if err != nil {
			t.Fatal(err)
		}
		for _, eventName := range []string{"StopFailure", "PermissionRequest"} {
			if !strings.Contains(string(raw), "external-"+eventName) {
				t.Fatalf(
					"migration %s removed external %s Hook: %s",
					version,
					eventName,
					raw,
				)
			}
		}
		receipt, err := adapterValue.readReceipt()
		if err != nil {
			t.Fatal(err)
		}
		if !equalStrings(receipt.Events, wantEvents) {
			t.Fatalf(
				"receipt events at %s = %#v, want %#v",
				version,
				receipt.Events,
				wantEvents,
			)
		}
	}

	assertVersion("2.0.19", []string{"Stop", "Notification"}, true)
	assertVersion("2.0.19", []string{"Stop", "Notification"}, false)
	assertVersion(
		"2.0.45",
		[]string{"Stop", "Notification", "PermissionRequest"},
		true,
	)
	assertVersion("2.1.78", claudeHookEvents, true)
	adapterValue.VersionOutput = func(string) (string, error) {
		return "2.0.45", nil
	}
	if verified, err := adapterValue.Verify(); err == nil ||
		verified.Installed ||
		!strings.Contains(strings.ToLower(err.Error()), "event set") {
		t.Fatalf(
			"same-form downgrade trusted stale event set: %#v err=%v",
			verified,
			err,
		)
	}
	assertVersion(
		"2.0.45",
		[]string{"Stop", "Notification", "PermissionRequest"},
		true,
	)
}

func TestClaudeUninstallUsesReceiptEventOwnershipAcrossVersionChange(t *testing.T) {
	adapterValue := newTestClaudeAdapter(t)
	if _, err := adapterValue.Install(false); err != nil {
		t.Fatal(err)
	}
	root, _, err := readJSONObject(adapterValue.settingsPath())
	if err != nil {
		t.Fatal(err)
	}
	hooks, err := objectField(root, "hooks", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, eventName := range claudeHookEvents {
		groups, err := arrayField(hooks, eventName, false)
		if err != nil {
			t.Fatal(err)
		}
		hooks[eventName] = append(groups, map[string]any{
			"hooks": []any{map[string]any{
				"type":    "command",
				"command": "external-" + eventName,
			}},
		})
	}
	if err := writeJSONObject(adapterValue.settingsPath(), root); err != nil {
		t.Fatal(err)
	}
	adapterValue.VersionOutput = func(string) (string, error) {
		return "2.0.19", nil
	}
	result, err := adapterValue.Uninstall(false)
	if err != nil || !result.Changed {
		t.Fatalf("uninstall after downgrade: %#v err=%v", result, err)
	}
	raw, err := os.ReadFile(adapterValue.settingsPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "--adapter") {
		t.Fatalf("receipt-owned hooks survived uninstall: %s", raw)
	}
	for _, eventName := range claudeHookEvents {
		if !strings.Contains(string(raw), "external-"+eventName) {
			t.Fatalf("external %s Hook was removed: %s", eventName, raw)
		}
	}
}

func TestClaudeReceiptEventOwnershipValidation(t *testing.T) {
	adapterValue := newTestClaudeAdapter(t)
	invocation, err := adapterValue.hookInvocation()
	if err != nil {
		t.Fatal(err)
	}
	legacy := claudeReceipt{
		Version:      1,
		Adapter:      claudeAdapterID,
		SettingsPath: adapterValue.settingsPath(),
		Command:      invocation.Executable,
		Args:         invocation.Args,
	}
	if err := adapterValue.writeReceipt(legacy); err != nil {
		t.Fatal(err)
	}
	normalized, err := adapterValue.readReceipt()
	if err != nil {
		t.Fatal(err)
	}
	if !equalStrings(normalized.Events, claudeHookEvents) {
		t.Fatalf(
			"legacy receipt events = %#v, want %#v",
			normalized.Events,
			claudeHookEvents,
		)
	}

	for _, events := range [][]string{
		{"Stop", "UnknownEvent"},
		{"Stop", "Stop"},
	} {
		invalid := legacy
		invalid.Events = events
		if err := adapterValue.writeReceipt(invalid); err != nil {
			t.Fatal(err)
		}
		if receipt, err := adapterValue.readReceipt(); err == nil {
			t.Fatalf("unsafe receipt was accepted: %#v", receipt)
		}
	}
}

func TestClaudeUnknownVersionRejectsExistingExecHandlerUntilMigration(t *testing.T) {
	adapterValue := newTestClaudeAdapter(t)
	if _, err := adapterValue.Install(false); err != nil {
		t.Fatal(err)
	}
	adapterValue.VersionOutput = func(string) (string, error) {
		return "", errors.New("version unavailable")
	}
	diagnosed := adapterValue.Diagnose()
	if diagnosed.Installed ||
		!strings.Contains(strings.ToLower(diagnosed.Message), "incompatible") {
		t.Fatalf("unknown version trusted exec handler: %#v", diagnosed)
	}
	migrated, err := adapterValue.Install(false)
	if err != nil || !migrated.Changed {
		t.Fatalf("unknown-version migration: %#v err=%v", migrated, err)
	}
	if verified, err := adapterValue.Verify(); err != nil || !verified.Installed {
		t.Fatalf("compatibility handler did not verify: %#v err=%v", verified, err)
	}
}

func TestClaudeInstallMigratesExactExistingHandlerWithoutReceipt(t *testing.T) {
	adapterValue := newTestClaudeAdapter(t)
	adapterValue.BridgeExecutable = testBridgePath(t)
	adapterValue.ActiveGeneration = 10
	invocation, err := adapterValue.hookInvocation()
	if err != nil {
		t.Fatal(err)
	}
	hooks := make(map[string]any, len(claudeHookEvents))
	for _, eventName := range claudeHookEvents {
		hooks[eventName] = []any{map[string]any{
			"hooks": []any{
				claudeHandler(invocation.Executable, invocation.Args),
			},
		}}
	}
	if err := writeJSONObject(adapterValue.settingsPath(), map[string]any{
		"hooks": hooks,
	}); err != nil {
		t.Fatal(err)
	}
	adapterValue.VersionOutput = func(string) (string, error) {
		return "2.0.19", nil
	}
	result, err := adapterValue.Install(false)
	if err != nil || !result.Changed {
		t.Fatalf("receipt-less migration: %#v err=%v", result, err)
	}
	value, err := os.ReadFile(adapterValue.settingsPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(value), "--adapter") != len(claudeBaselineHookEvents) ||
		strings.Contains(string(value), `"args"`) {
		t.Fatalf("receipt-less migration duplicated handlers: %s", value)
	}
}

func TestClaudeUnknownVersionUsesDiagnosedCompatibilityMode(t *testing.T) {
	adapterValue := newTestClaudeAdapter(t)
	adapterValue.VersionOutput = func(string) (string, error) {
		return "", errors.New("version unavailable")
	}
	result, err := adapterValue.Install(false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(result.Message), "version") ||
		!strings.Contains(strings.ToLower(result.Message), "compatibility") {
		t.Fatalf("install omitted unknown-version diagnosis: %#v", result)
	}
	diagnosed := adapterValue.Diagnose()
	if !diagnosed.Installed ||
		!strings.Contains(strings.ToLower(diagnosed.Message), "version") ||
		!strings.Contains(strings.ToLower(diagnosed.Message), "compatibility") {
		t.Fatalf("diagnose omitted compatibility mode: %#v", diagnosed)
	}
}

func TestClaudeDefaultPlatformIsHostPlatform(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, ".claude"))
	adapterValue, err := NewClaudeAdapter(
		filepath.Join(root, "agentbell"),
		filepath.Join(root, "state"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if adapterValue.GOOS != runtime.GOOS {
		t.Fatalf("GOOS = %q, want %q", adapterValue.GOOS, runtime.GOOS)
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
