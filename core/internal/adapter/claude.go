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

const claudeAdapterID = "claude-code"

var claudeHookEvents = []string{"Stop", "StopFailure", "Notification", "PermissionRequest"}

type ClaudeAdapter struct {
	Executable string
	StateDir   string
	ClaudeHome string
	Now        func() time.Time
	LookPath   func(string) (string, error)
}

type claudeReceipt struct {
	Version      int       `json:"version"`
	Adapter      string    `json:"adapter"`
	SettingsPath string    `json:"settingsPath"`
	Command      string    `json:"command"`
	Args         []string  `json:"args"`
	Backup       string    `json:"backup,omitempty"`
	InstalledAt  time.Time `json:"installedAt"`
}

func NewClaudeAdapter(executable, stateDir string) (*ClaudeAdapter, error) {
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
	home := os.Getenv("CLAUDE_CONFIG_DIR")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		home = filepath.Join(userHome, ".claude")
	}
	return &ClaudeAdapter{
		Executable: absolute,
		StateDir:   stateDir,
		ClaudeHome: home,
		Now:        time.Now,
		LookPath:   exec.LookPath,
	}, nil
}

func (adapter *ClaudeAdapter) Detect() bool {
	if _, err := adapter.lookPath()("claude"); err == nil {
		return true
	}
	_, err := os.Stat(adapter.ClaudeHome)
	return err == nil
}

func (adapter *ClaudeAdapter) Plan() AdapterPlan {
	return AdapterPlan{
		Adapter:    claudeAdapterID,
		Detected:   adapter.Detect(),
		HookPath:   adapter.settingsPath(),
		Executable: adapter.Executable,
		Changes: []string{
			"merge AgentBell exec-form hooks into Stop, StopFailure, Notification and PermissionRequest",
			"share the user-level settings hooks across Claude Code CLI and Desktop local sessions",
			"write an ownership receipt for precise uninstall",
		},
	}
}

func (adapter *ClaudeAdapter) Install(dryRun bool) (AdapterResult, error) {
	result := AdapterResult{
		Adapter: claudeAdapterID, Detected: adapter.Detect(), HookPath: adapter.settingsPath(),
	}
	root, exists, err := readJSONObject(adapter.settingsPath())
	if err != nil {
		return result, err
	}
	command, args, err := adapter.command()
	if err != nil {
		return result, err
	}
	changed, err := mergeClaudeHooks(root, command, args)
	if err != nil {
		return result, err
	}
	result.Installed = true
	result.Changed = changed
	if dryRun {
		result.Message = "AgentBell Claude Code hooks would be installed for CLI and Desktop local sessions"
		return result, nil
	}
	if !changed {
		result.Message = "AgentBell Claude Code hooks are already installed"
		return result, nil
	}

	if err := os.MkdirAll(filepath.Dir(adapter.settingsPath()), 0o700); err != nil {
		return result, err
	}
	backup := ""
	if exists {
		backup, err = adapter.backup(adapter.settingsPath())
		if err != nil {
			return result, err
		}
	}
	if err := writeJSONObject(adapter.settingsPath(), root); err != nil {
		return result, err
	}
	receipt := claudeReceipt{
		Version:      1,
		Adapter:      claudeAdapterID,
		SettingsPath: adapter.settingsPath(),
		Command:      command,
		Args:         args,
		Backup:       backup,
		InstalledAt:  adapter.now().UTC(),
	}
	if err := adapter.writeReceipt(receipt); err != nil {
		return result, err
	}
	result.Backup = backup
	result.Message = "AgentBell Claude Code hooks are installed for CLI and Desktop local sessions"
	return result, nil
}

func (adapter *ClaudeAdapter) Verify() (AdapterResult, error) {
	result := AdapterResult{
		Adapter: claudeAdapterID, Detected: adapter.Detect(), HookPath: adapter.settingsPath(),
	}
	root, _, err := readJSONObject(adapter.settingsPath())
	if err != nil {
		return result, err
	}
	command, args, commandErr := adapter.command()
	receipt, receiptErr := adapter.readReceipt()
	if receiptErr == nil {
		command = receipt.Command
		args = receipt.Args
	}
	if commandErr != nil && receiptErr != nil {
		return result, errors.Join(commandErr, receiptErr)
	}
	for _, eventName := range claudeHookEvents {
		if !hasClaudeHook(root, eventName, command, args) {
			return result, fmt.Errorf("AgentBell Claude Code hook for %s is missing", eventName)
		}
	}
	result.Installed = true
	result.Message = "AgentBell Claude Code hooks are installed for CLI and Desktop local sessions"
	return result, nil
}

func (adapter *ClaudeAdapter) Uninstall(dryRun bool) (AdapterResult, error) {
	result := AdapterResult{
		Adapter: claudeAdapterID, Detected: adapter.Detect(), HookPath: adapter.settingsPath(),
	}
	root, exists, err := readJSONObject(adapter.settingsPath())
	if err != nil {
		return result, err
	}
	if !exists {
		result.Message = "Claude Code settings file does not exist"
		return result, nil
	}
	command, args, commandErr := adapter.command()
	receipt, receiptErr := adapter.readReceipt()
	if receiptErr == nil {
		command = receipt.Command
		args = receipt.Args
	}
	if commandErr != nil && receiptErr != nil {
		return result, errors.Join(commandErr, receiptErr)
	}
	changed, err := removeClaudeHooks(root, command, args)
	if err != nil {
		return result, err
	}
	result.Changed = changed
	if dryRun {
		if changed {
			result.Message = "AgentBell Claude Code hooks would be uninstalled"
		} else {
			result.Message = "AgentBell Claude Code hooks are not installed"
		}
		return result, nil
	}
	if !changed {
		result.Message = "AgentBell Claude Code hooks are not installed"
		return result, nil
	}
	backup, err := adapter.backup(adapter.settingsPath())
	if err != nil {
		return result, err
	}
	if err := writeJSONObject(adapter.settingsPath(), root); err != nil {
		return result, err
	}
	_ = os.Remove(adapter.receiptPath())
	result.Backup = backup
	result.Message = "AgentBell Claude Code hooks are uninstalled"
	return result, nil
}

func (adapter *ClaudeAdapter) Diagnose() AdapterResult {
	result, err := adapter.Verify()
	if err != nil {
		result.Message = err.Error()
		return result
	}
	proof, verified := runtimeProofAfterConfig(
		adapter.StateDir,
		claudeAdapterID,
		adapter.settingsPath(),
	)
	result.RuntimeVerified = verified
	if !proof.LastSeen.IsZero() {
		result.LastSeen = proof.LastSeen.Format(time.RFC3339Nano)
	}
	if verified {
		result.Message = "AgentBell Claude Code hooks have run since the last settings change"
	} else {
		result.Message = "Claude Code hooks are installed but not yet observed after the last settings change; complete a new CLI or Desktop local turn"
	}
	return result
}

func (adapter *ClaudeAdapter) settingsPath() string {
	return filepath.Join(adapter.ClaudeHome, "settings.json")
}

func (adapter *ClaudeAdapter) command() (string, []string, error) {
	if strings.ContainsAny(adapter.Executable, "\x00\n\r") {
		return "", nil, errors.New("AgentBell executable path contains unsupported characters")
	}
	return adapter.Executable, []string{
		"emit",
		"--adapter", claudeAdapterID,
		"--surface", "cli",
		"--runtime", "host",
		"--stdin",
		"--fail-open",
	}, nil
}

func (adapter *ClaudeAdapter) backup(source string) (string, error) {
	value, err := os.ReadFile(source)
	if err != nil {
		return "", err
	}
	directory := filepath.Join(adapter.StateDir, "adapters", claudeAdapterID, "backups")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	name := fmt.Sprintf(
		"settings-%s-%s.json",
		adapter.now().UTC().Format("20060102T150405.000000000Z"),
		hashBytes(value)[:12],
	)
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, value, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func (adapter *ClaudeAdapter) receiptPath() string {
	return filepath.Join(adapter.StateDir, "adapters", claudeAdapterID, "receipt.json")
}

func (adapter *ClaudeAdapter) writeReceipt(receipt claudeReceipt) error {
	return writeJSONFile(adapter.receiptPath(), receipt)
}

func (adapter *ClaudeAdapter) readReceipt() (claudeReceipt, error) {
	value, err := os.ReadFile(adapter.receiptPath())
	if err != nil {
		return claudeReceipt{}, err
	}
	var receipt claudeReceipt
	if err := json.Unmarshal(value, &receipt); err != nil {
		return claudeReceipt{}, err
	}
	if receipt.Version != 1 || receipt.Adapter != claudeAdapterID ||
		receipt.Command == "" || len(receipt.Args) == 0 {
		return claudeReceipt{}, errors.New("invalid Claude Code adapter receipt")
	}
	return receipt, nil
}

func (adapter *ClaudeAdapter) now() time.Time {
	if adapter.Now != nil {
		return adapter.Now()
	}
	return time.Now()
}

func (adapter *ClaudeAdapter) lookPath() func(string) (string, error) {
	if adapter.LookPath != nil {
		return adapter.LookPath
	}
	return exec.LookPath
}

func mergeClaudeHooks(root map[string]any, command string, args []string) (bool, error) {
	hooks, err := objectField(root, "hooks", true)
	if err != nil {
		return false, err
	}
	changed := false
	for _, eventName := range claudeHookEvents {
		if hasClaudeHook(root, eventName, command, args) {
			continue
		}
		groups, err := arrayField(hooks, eventName, true)
		if err != nil {
			return false, err
		}
		group := map[string]any{
			"hooks": []any{claudeHandler(command, args)},
		}
		if eventName == "Notification" {
			group["matcher"] = "idle_prompt|agent_needs_input"
		}
		hooks[eventName] = append(groups, group)
		changed = true
	}
	return changed, nil
}

func removeClaudeHooks(root map[string]any, command string, args []string) (bool, error) {
	hooks, err := objectField(root, "hooks", false)
	if err != nil {
		return false, err
	}
	if hooks == nil {
		return false, nil
	}
	changed := false
	for _, eventName := range claudeHookEvents {
		groups, err := arrayField(hooks, eventName, false)
		if err != nil {
			return false, err
		}
		if groups == nil {
			continue
		}
		filteredGroups := make([]any, 0, len(groups))
		for _, groupValue := range groups {
			group, ok := groupValue.(map[string]any)
			if !ok {
				return false, fmt.Errorf("hooks.%s contains a non-object matcher group", eventName)
			}
			handlers, err := arrayField(group, "hooks", false)
			if err != nil {
				return false, err
			}
			filteredHandlers := make([]any, 0, len(handlers))
			for _, handlerValue := range handlers {
				handler, ok := handlerValue.(map[string]any)
				if ok && matchesClaudeHandler(handler, command, args) {
					changed = true
					continue
				}
				filteredHandlers = append(filteredHandlers, handlerValue)
			}
			if len(filteredHandlers) == 0 && len(handlers) > 0 {
				continue
			}
			group["hooks"] = filteredHandlers
			filteredGroups = append(filteredGroups, group)
		}
		if len(filteredGroups) == 0 {
			delete(hooks, eventName)
		} else {
			hooks[eventName] = filteredGroups
		}
	}
	return changed, nil
}

func hasClaudeHook(root map[string]any, eventName, command string, args []string) bool {
	hooks, err := objectField(root, "hooks", false)
	if err != nil || hooks == nil {
		return false
	}
	groups, err := arrayField(hooks, eventName, false)
	if err != nil {
		return false
	}
	for _, groupValue := range groups {
		group, ok := groupValue.(map[string]any)
		if !ok {
			continue
		}
		handlers, _ := arrayField(group, "hooks", false)
		for _, handlerValue := range handlers {
			handler, ok := handlerValue.(map[string]any)
			if ok && matchesClaudeHandler(handler, command, args) {
				return true
			}
		}
	}
	return false
}

func claudeHandler(command string, args []string) map[string]any {
	values := make([]any, 0, len(args))
	for _, value := range args {
		values = append(values, value)
	}
	return map[string]any{
		"type":    "command",
		"command": command,
		"args":    values,
		"timeout": float64(5),
	}
}

func matchesClaudeHandler(handler map[string]any, command string, args []string) bool {
	if handler["type"] != "command" || handler["command"] != command {
		return false
	}
	rawArgs, ok := handler["args"].([]any)
	if !ok || len(rawArgs) != len(args) {
		return false
	}
	for index, expected := range args {
		if rawArgs[index] != expected {
			return false
		}
	}
	return true
}
