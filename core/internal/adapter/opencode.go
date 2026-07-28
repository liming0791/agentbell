package adapter

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const opencodeAdapterID = "opencode"

var opencodeHookEvents = []string{"session.idle", "session.error", "permission.asked"}

// opencodePluginMarker is embedded in the generated plugin so Verify and
// Uninstall can identify AgentBell ownership without a receipt.
const opencodePluginMarker = "// agentbell:opencode:owner"

type OpenCodeAdapter struct {
	Executable   string
	StateDir     string
	OpenCodeHome string
	Now          func() time.Time
	LookPath     func(string) (string, error)
}

type opencodeReceipt struct {
	Version     int       `json:"version"`
	Adapter     string    `json:"adapter"`
	PluginPath  string    `json:"pluginPath"`
	Executable  string    `json:"executable"`
	Backup      string    `json:"backup,omitempty"`
	InstalledAt time.Time `json:"installedAt"`
}

func NewOpenCodeAdapter(executable, stateDir string) (*OpenCodeAdapter, error) {
	if executable == "" {
		resolved, err := os.Executable()
		if err != nil {
			return nil, err
		}
		executable = resolved
	}
	absolute, err := filepath.Abs(executable)
	if err != nil {
		return nil, err
	}
	home := os.Getenv("OPENCODE_CONFIG_DIR")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		home = filepath.Join(userHome, ".config", "opencode")
	}
	return &OpenCodeAdapter{
		Executable:   absolute,
		StateDir:     stateDir,
		OpenCodeHome: home,
		Now:          time.Now,
		LookPath:     exec.LookPath,
	}, nil
}

func (adapter *OpenCodeAdapter) Detect() bool {
	if _, err := adapter.lookPath()("opencode"); err == nil {
		return true
	}
	_, err := os.Stat(adapter.OpenCodeHome)
	return err == nil
}

func (adapter *OpenCodeAdapter) Plan() AdapterPlan {
	return AdapterPlan{
		Adapter:    opencodeAdapterID,
		Detected:   adapter.Detect(),
		HookPath:   adapter.pluginPath(),
		Executable: adapter.Executable,
		Changes: []string{
			"write AgentBell global plugin to subscribe session.idle, session.error and permission.asked",
			"the plugin is shared across OpenCode CLI, TUI and Desktop",
			"write an ownership receipt for precise uninstall",
		},
	}
}

func (adapter *OpenCodeAdapter) Install(dryRun bool) (AdapterResult, error) {
	result := AdapterResult{
		Adapter: opencodeAdapterID, Detected: adapter.Detect(), HookPath: adapter.pluginPath(),
	}
	content, err := adapter.pluginContent()
	if err != nil {
		return result, err
	}
	existing, exists, err := readOptionalFile(adapter.pluginPath())
	if err != nil {
		return result, err
	}
	changed := !exists || string(existing) != content
	result.Installed = true
	result.Changed = changed
	if dryRun {
		result.Message = "AgentBell OpenCode plugin would be installed for CLI, TUI and Desktop"
		return result, nil
	}
	if !changed {
		result.Message = "AgentBell OpenCode plugin is already installed"
		return result, nil
	}

	if err := os.MkdirAll(filepath.Dir(adapter.pluginPath()), 0o700); err != nil {
		return result, err
	}
	backup := ""
	if exists && isAgentBellPlugin(string(existing)) {
		backup, err = adapter.backup(adapter.pluginPath())
		if err != nil {
			return result, err
		}
	} else if exists {
		return result, errors.New(
			"plugins/agentbell.js already exists but is not owned by AgentBell; remove it manually",
		)
	}
	if err := writeFileAtomic(adapter.pluginPath(), []byte(content)); err != nil {
		return result, err
	}
	receipt := opencodeReceipt{
		Version:     1,
		Adapter:     opencodeAdapterID,
		PluginPath:  adapter.pluginPath(),
		Executable:  adapter.Executable,
		Backup:      backup,
		InstalledAt: adapter.now().UTC(),
	}
	if err := adapter.writeReceipt(receipt); err != nil {
		return result, err
	}
	result.Backup = backup
	result.Message = "AgentBell OpenCode plugin is installed; restart OpenCode to load it"
	return result, nil
}

func (adapter *OpenCodeAdapter) Verify() (AdapterResult, error) {
	result := AdapterResult{
		Adapter: opencodeAdapterID, Detected: adapter.Detect(), HookPath: adapter.pluginPath(),
	}
	raw, err := os.ReadFile(adapter.pluginPath())
	if errors.Is(err, os.ErrNotExist) {
		return result, errors.New("AgentBell OpenCode plugin is not installed")
	}
	if err != nil {
		return result, err
	}
	if !isAgentBellPlugin(string(raw)) {
		return result, errors.New("plugins/agentbell.js is not owned by AgentBell")
	}
	expected, contentErr := adapter.pluginContent()
	if contentErr == nil && string(raw) == expected {
		result.Installed = true
		result.Message = "AgentBell OpenCode plugin is installed"
		return result, nil
	}
	// Content mismatch: either outdated or executable moved. Fall back to receipt.
	receipt, receiptErr := adapter.readReceipt()
	if receiptErr != nil {
		if contentErr != nil {
			return result, errors.Join(contentErr, receiptErr)
		}
		return result, errors.New("AgentBell OpenCode plugin is outdated; run install to update")
	}
	if !hasOpenCodeExecutable(string(raw), receipt.Executable) {
		return result, errors.New("AgentBell OpenCode plugin executable does not match the receipt")
	}
	result.Installed = true
	result.Message = "AgentBell OpenCode plugin is installed"
	return result, nil
}

func (adapter *OpenCodeAdapter) Uninstall(dryRun bool) (AdapterResult, error) {
	result := AdapterResult{
		Adapter: opencodeAdapterID, Detected: adapter.Detect(), HookPath: adapter.pluginPath(),
	}
	raw, exists, err := readOptionalFile(adapter.pluginPath())
	if err != nil {
		return result, err
	}
	if !exists {
		result.Message = "OpenCode plugin file does not exist"
		return result, nil
	}
	if !isAgentBellPlugin(string(raw)) {
		result.Message = "plugins/agentbell.js is not owned by AgentBell"
		return result, nil
	}
	result.Changed = true
	if dryRun {
		result.Message = "AgentBell OpenCode plugin would be uninstalled"
		return result, nil
	}
	backup, err := adapter.backup(adapter.pluginPath())
	if err != nil {
		return result, err
	}
	if err := os.Remove(adapter.pluginPath()); err != nil {
		return result, err
	}
	_ = os.Remove(adapter.receiptPath())
	result.Backup = backup
	result.Message = "AgentBell OpenCode plugin is uninstalled"
	return result, nil
}

func (adapter *OpenCodeAdapter) Diagnose() AdapterResult {
	result, err := adapter.Verify()
	if err != nil {
		result.Message = err.Error()
		return result
	}
	proof, verified := runtimeProofAfterConfig(
		adapter.StateDir,
		opencodeAdapterID,
		adapter.pluginPath(),
	)
	result.RuntimeVerified = verified
	if !proof.LastSeen.IsZero() {
		result.LastSeen = proof.LastSeen.Format(time.RFC3339Nano)
	}
	if verified {
		result.Message = "AgentBell OpenCode plugin has run since the last config change"
	} else {
		result.Message = "OpenCode plugin is installed but not yet observed after the last config change; restart OpenCode and complete a session"
	}
	return result
}

func (adapter *OpenCodeAdapter) pluginPath() string {
	return filepath.Join(adapter.OpenCodeHome, "plugins", "agentbell.js")
}

func (adapter *OpenCodeAdapter) pluginContent() (string, error) {
	if strings.ContainsAny(adapter.Executable, "\x00\n\r") {
		return "", errors.New("AgentBell executable path contains unsupported characters")
	}
	return opencodePluginTemplate(adapter.Executable), nil
}

func opencodePluginTemplate(executable string) string {
	var builder strings.Builder
	builder.WriteString(opencodePluginMarker + "\n")
	builder.WriteString(`import { spawn } from "node:child_process";

export const AgentBell = async () => {
  const executable = `)
	builder.WriteString(jsonString(executable))
	builder.WriteString(`;
  const events = new Set([`)
	for index, event := range opencodeHookEvents {
		if index > 0 {
			builder.WriteString(", ")
		}
		builder.WriteString(jsonString(event))
	}
	builder.WriteString(`]);
  return {
    event: async ({ event }) => {
      if (!events.has(event.type)) return;
      const input = JSON.stringify(event);
      await new Promise((resolve) => {
        const child = spawn(executable, [
          "emit",
          "--adapter", "opencode",
          "--surface", "cli",
          "--runtime", "host",
          "--stdin",
          "--fail-open",
        ], { stdio: ["pipe", "ignore", "ignore"] });
        child.on("error", () => resolve());
        child.stdin.on("error", () => {});
        child.once("close", () => resolve());
        child.stdin.end(input);
      });
    },
  };
};
`)
	return builder.String()
}

func isAgentBellPlugin(content string) bool {
	return strings.Contains(content, opencodePluginMarker)
}

func hasOpenCodeExecutable(content, executable string) bool {
	const prefix = "  const executable = "
	declaration := prefix + jsonString(executable) + ";\n"
	return strings.Count(content, prefix) == 1 &&
		strings.Contains(content, declaration)
}

func jsonString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func (adapter *OpenCodeAdapter) backup(source string) (string, error) {
	value, err := os.ReadFile(source)
	if err != nil {
		return "", err
	}
	directory := filepath.Join(adapter.StateDir, "adapters", opencodeAdapterID, "backups")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	name := fmt.Sprintf(
		"agentbell-%s-%s.js",
		adapter.now().UTC().Format("20060102T150405.000000000Z"),
		hashBytes(value)[:12],
	)
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, value, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func (adapter *OpenCodeAdapter) receiptPath() string {
	return filepath.Join(adapter.StateDir, "adapters", opencodeAdapterID, "receipt.json")
}

func (adapter *OpenCodeAdapter) writeReceipt(receipt opencodeReceipt) error {
	return writeJSONFile(adapter.receiptPath(), receipt)
}

func (adapter *OpenCodeAdapter) readReceipt() (opencodeReceipt, error) {
	value, err := os.ReadFile(adapter.receiptPath())
	if err != nil {
		return opencodeReceipt{}, err
	}
	var receipt opencodeReceipt
	if err := json.Unmarshal(value, &receipt); err != nil {
		return opencodeReceipt{}, err
	}
	if receipt.Version != 1 || receipt.Adapter != opencodeAdapterID ||
		receipt.Executable == "" {
		return opencodeReceipt{}, errors.New("invalid OpenCode adapter receipt")
	}
	return receipt, nil
}

func (adapter *OpenCodeAdapter) now() time.Time {
	if adapter.Now != nil {
		return adapter.Now()
	}
	return time.Now()
}

func (adapter *OpenCodeAdapter) lookPath() func(string) (string, error) {
	if adapter.LookPath != nil {
		return adapter.LookPath
	}
	return exec.LookPath
}
