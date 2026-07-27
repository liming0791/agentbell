package adapter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testBridgePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(
		t.TempDir(),
		"AgentBell Data",
		"bin",
		"bridge",
		"v1",
		"agentbell-bridge",
	)
}

func encodedConfigPath(value string) string {
	return strings.ReplaceAll(value, `\`, `\\`)
}

func TestStableBridgeCommandsDoNotContainVersionedCore(t *testing.T) {
	t.Run("codex", func(t *testing.T) {
		adapterValue := newTestCodexAdapter(t)
		core := adapterValue.Executable
		adapterValue.BridgeExecutable = testBridgePath(t)
		adapterValue.ActiveGeneration = 7
		command, commandWindows, err := adapterValue.commands()
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range []string{command, commandWindows} {
			if strings.Contains(value, core) ||
				!strings.Contains(value, adapterValue.BridgeExecutable) ||
				!strings.Contains(value, " hook-v1 --adapter codex ") {
				t.Fatalf("unexpected stable Codex command: %q", value)
			}
		}
	})

	t.Run("claude", func(t *testing.T) {
		adapterValue := newTestClaudeAdapter(t)
		core := adapterValue.Executable
		adapterValue.BridgeExecutable = testBridgePath(t)
		adapterValue.ActiveGeneration = 7
		command, args, err := adapterValue.command()
		if err != nil {
			t.Fatal(err)
		}
		if command != adapterValue.BridgeExecutable ||
			command == core ||
			len(args) == 0 ||
			args[0] != "hook-v1" {
			t.Fatalf("unexpected stable Claude command: %q %#v", command, args)
		}
	})

	t.Run("kimi", func(t *testing.T) {
		adapterValue := newTestKimiAdapter(t)
		core := adapterValue.Executable
		adapterValue.BridgeExecutable = testBridgePath(t)
		adapterValue.ActiveGeneration = 7
		command, err := adapterValue.command()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(command, core) ||
			!strings.Contains(command, adapterValue.BridgeExecutable) ||
			!strings.Contains(command, " hook-v1 --adapter kimi-code ") {
			t.Fatalf("unexpected stable Kimi command: %q", command)
		}
	})
}

func TestStableBridgeRequiresAbsolutePathAndActiveGeneration(t *testing.T) {
	adapterValue := newTestClaudeAdapter(t)
	adapterValue.BridgeExecutable = "relative/agentbell-bridge"
	adapterValue.ActiveGeneration = 1
	if _, _, err := adapterValue.command(); err == nil {
		t.Fatal("relative stable bridge path must fail")
	}
	adapterValue.BridgeExecutable = testBridgePath(t)
	adapterValue.ActiveGeneration = 0
	if _, _, err := adapterValue.command(); err == nil {
		t.Fatal("stable bridge without active generation must fail")
	}
	adapterValue.ActiveGeneration = 1
	if plan := adapterValue.Plan(); plan.Executable != adapterValue.BridgeExecutable {
		t.Fatalf("plan did not expose the Hook executable: %#v", plan)
	}
}

func TestStableBridgeMigratesOwnedLegacyHooksOnce(t *testing.T) {
	t.Run("codex", func(t *testing.T) {
		adapterValue := newTestCodexAdapter(t)
		if _, err := adapterValue.Install(false); err != nil {
			t.Fatal(err)
		}
		legacyCore := adapterValue.Executable
		adapterValue.BridgeExecutable = testBridgePath(t)
		adapterValue.ActiveGeneration = 11
		migrated, err := adapterValue.Install(false)
		if err != nil || !migrated.Changed {
			t.Fatalf("migrate: %#v err=%v", migrated, err)
		}
		firstBytes, err := os.ReadFile(adapterValue.hookPath())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(firstBytes), encodedConfigPath(legacyCore)) ||
			!strings.Contains(
				string(firstBytes),
				encodedConfigPath(adapterValue.BridgeExecutable),
			) {
			t.Fatalf("legacy Codex command survived: %s", firstBytes)
		}
		receipt, err := adapterValue.readReceipt()
		if err != nil || receipt.BridgeProtocol != 1 {
			t.Fatalf("bridge receipt: %#v err=%v", receipt, err)
		}
		adapterValue.Executable = filepath.Join(t.TempDir(), "bin", "v-next", "agentbell")
		adapterValue.ActiveGeneration = 12
		second, err := adapterValue.Install(false)
		if err != nil || second.Changed {
			t.Fatalf("stable reinstall: %#v err=%v", second, err)
		}
		secondBytes, _ := os.ReadFile(adapterValue.hookPath())
		if string(firstBytes) != string(secondBytes) {
			t.Fatal("Codex trusted Hook bytes changed after Core activation")
		}
		if removed, err := adapterValue.Uninstall(false); err != nil || !removed.Changed {
			t.Fatalf("bridge uninstall: %#v err=%v", removed, err)
		}
	})

	t.Run("claude", func(t *testing.T) {
		adapterValue := newTestClaudeAdapter(t)
		if _, err := adapterValue.Install(false); err != nil {
			t.Fatal(err)
		}
		legacyCore := adapterValue.Executable
		adapterValue.BridgeExecutable = testBridgePath(t)
		adapterValue.ActiveGeneration = 21
		migrated, err := adapterValue.Install(false)
		if err != nil || !migrated.Changed {
			t.Fatalf("migrate: %#v err=%v", migrated, err)
		}
		firstBytes, _ := os.ReadFile(adapterValue.settingsPath())
		if strings.Contains(string(firstBytes), encodedConfigPath(legacyCore)) ||
			!strings.Contains(
				string(firstBytes),
				encodedConfigPath(adapterValue.BridgeExecutable),
			) {
			t.Fatalf("legacy Claude command survived: %s", firstBytes)
		}
		receipt, err := adapterValue.readReceipt()
		if err != nil || receipt.BridgeProtocol != 1 {
			t.Fatalf("bridge receipt: %#v err=%v", receipt, err)
		}
		adapterValue.Executable = filepath.Join(t.TempDir(), "bin", "v-next", "agentbell")
		adapterValue.ActiveGeneration = 22
		second, err := adapterValue.Install(false)
		if err != nil || second.Changed {
			t.Fatalf("stable reinstall: %#v err=%v", second, err)
		}
		secondBytes, _ := os.ReadFile(adapterValue.settingsPath())
		if string(firstBytes) != string(secondBytes) {
			t.Fatal("Claude Hook bytes changed after Core activation")
		}
		if removed, err := adapterValue.Uninstall(false); err != nil || !removed.Changed {
			t.Fatalf("bridge uninstall: %#v err=%v", removed, err)
		}
	})

	t.Run("kimi", func(t *testing.T) {
		adapterValue := newTestKimiAdapter(t)
		if _, err := adapterValue.Install(false); err != nil {
			t.Fatal(err)
		}
		legacyCore := adapterValue.Executable
		adapterValue.BridgeExecutable = testBridgePath(t)
		adapterValue.ActiveGeneration = 31
		migrated, err := adapterValue.Install(false)
		if err != nil || !migrated.Changed ||
			!strings.Contains(migrated.Message, "new Kimi session") {
			t.Fatalf("migrate: %#v err=%v", migrated, err)
		}
		firstBytes, _ := os.ReadFile(adapterValue.configPath())
		if strings.Contains(string(firstBytes), encodedConfigPath(legacyCore)) ||
			!strings.Contains(
				string(firstBytes),
				encodedConfigPath(adapterValue.BridgeExecutable),
			) {
			t.Fatalf("legacy Kimi command survived: %s", firstBytes)
		}
		receipt, err := adapterValue.readReceipt()
		if err != nil || receipt.BridgeProtocol != 1 {
			t.Fatalf("bridge receipt: %#v err=%v", receipt, err)
		}
		adapterValue.Executable = filepath.Join(t.TempDir(), "bin", "v-next", "agentbell")
		adapterValue.ActiveGeneration = 32
		second, err := adapterValue.Install(false)
		if err != nil || second.Changed {
			t.Fatalf("stable reinstall: %#v err=%v", second, err)
		}
		secondBytes, _ := os.ReadFile(adapterValue.configPath())
		if string(firstBytes) != string(secondBytes) {
			t.Fatal("Kimi Hook bytes changed after Core activation")
		}
		if removed, err := adapterValue.Uninstall(false); err != nil || !removed.Changed {
			t.Fatalf("bridge uninstall: %#v err=%v", removed, err)
		}
	})
}

func TestMigratedBridgeDiagnoseRequiresActiveGeneration(t *testing.T) {
	tests := []struct {
		name      string
		install   func(t *testing.T) (string, string, uint64, func() AdapterResult)
		eventName string
	}{
		{
			name: "codex",
			install: func(t *testing.T) (string, string, uint64, func() AdapterResult) {
				value := newTestCodexAdapter(t)
				value.BridgeExecutable = testBridgePath(t)
				value.ActiveGeneration = 41
				if _, err := value.Install(false); err != nil {
					t.Fatal(err)
				}
				return value.StateDir, value.hookPath(), value.ActiveGeneration, value.Diagnose
			},
			eventName: "task.completed",
		},
		{
			name: "claude",
			install: func(t *testing.T) (string, string, uint64, func() AdapterResult) {
				value := newTestClaudeAdapter(t)
				value.BridgeExecutable = testBridgePath(t)
				value.ActiveGeneration = 42
				if _, err := value.Install(false); err != nil {
					t.Fatal(err)
				}
				return value.StateDir, value.settingsPath(), value.ActiveGeneration, value.Diagnose
			},
			eventName: "task.completed",
		},
		{
			name: "kimi",
			install: func(t *testing.T) (string, string, uint64, func() AdapterResult) {
				value := newTestKimiAdapter(t)
				value.BridgeExecutable = testBridgePath(t)
				value.ActiveGeneration = 43
				if _, err := value.Install(false); err != nil {
					t.Fatal(err)
				}
				return value.StateDir, value.configPath(), value.ActiveGeneration, value.Diagnose
			},
			eventName: "task.completed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stateDir, configPath, generation, diagnose := test.install(t)
			info, err := os.Stat(configPath)
			if err != nil {
				t.Fatal(err)
			}
			seenAt := info.ModTime().Add(time.Second)
			adapterID := test.name
			if adapterID == "claude" {
				adapterID = claudeAdapterID
			} else if adapterID == "kimi" {
				adapterID = kimiAdapterID
			}
			if err := RecordRuntimeProof(
				stateDir,
				adapterID,
				test.eventName,
				seenAt,
			); err != nil {
				t.Fatal(err)
			}
			if result := diagnose(); result.RuntimeVerified {
				t.Fatalf("legacy proof verified migrated bridge: %#v", result)
			}
			if err := RecordRuntimeProofWithContext(
				stateDir,
				adapterID,
				test.eventName,
				seenAt,
				RuntimeProofContext{
					BridgeProtocol:       1,
					CoreVersion:          "0.3.0",
					ActivationGeneration: generation + 1,
				},
			); err != nil {
				t.Fatal(err)
			}
			if result := diagnose(); result.RuntimeVerified {
				t.Fatalf("wrong generation verified migrated bridge: %#v", result)
			}
			if err := RecordRuntimeProofWithContext(
				stateDir,
				adapterID,
				test.eventName,
				seenAt,
				RuntimeProofContext{
					BridgeProtocol:       1,
					CoreVersion:          "0.3.0",
					ActivationGeneration: generation,
				},
			); err != nil {
				t.Fatal(err)
			}
			if result := diagnose(); !result.RuntimeVerified {
				t.Fatalf("active bridge proof was rejected: %#v", result)
			}
		})
	}
}

func TestLegacyDirectCoreDiagnoseRemainsCompatible(t *testing.T) {
	adapterValue := newTestClaudeAdapter(t)
	if _, err := adapterValue.Install(false); err != nil {
		t.Fatal(err)
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
	if result := adapterValue.Diagnose(); !result.RuntimeVerified {
		t.Fatalf("legacy direct-Core proof lost compatibility: %#v", result)
	}
}
