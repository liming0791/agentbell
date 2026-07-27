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

const qoderAdapterID = "qoder"

var qoderHookEvents = []string{"Stop", "PostToolUseFailure"}

type QoderAdapter struct {
	Executable string
	StateDir   string
	QoderHome  string
	Now        func() time.Time
	LookPath   func(string) (string, error)
}

type qoderReceipt struct {
	Version      int       `json:"version"`
	Adapter      string    `json:"adapter"`
	SettingsPath string    `json:"settingsPath"`
	Command      string    `json:"command"`
	Args         []string  `json:"args"`
	Backup       string    `json:"backup,omitempty"`
	InstalledAt  time.Time `json:"installedAt"`
}

func NewQoderAdapter(executable, stateDir string) (*QoderAdapter, error) {
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
	home := os.Getenv("QODER_CONFIG_DIR")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		home = filepath.Join(userHome, ".qoder")
	}
	return &QoderAdapter{
		Executable: absolute,
		StateDir:   stateDir,
		QoderHome:  home,
		Now:        time.Now,
		LookPath:   exec.LookPath,
	}, nil
}

func (adapter *QoderAdapter) Detect() bool {
	if _, err := adapter.lookPath()("qoder"); err == nil {
		return true
	}
	_, err := os.Stat(adapter.QoderHome)
	return err == nil
}

func (adapter *QoderAdapter) Plan() AdapterPlan {
	return AdapterPlan{
		Adapter:    qoderAdapterID,
		Detected:   adapter.Detect(),
		HookPath:   adapter.settingsPath(),
		Executable: adapter.Executable,
		Changes: []string{
			"merge AgentBell exec-form hooks into Stop and PostToolUseFailure",
			"share the user-level settings hooks across Qoder CLI, IDE and JetBrains sessions",
			"write an ownership receipt for precise uninstall",
		},
	}
}

func (adapter *QoderAdapter) Install(dryRun bool) (AdapterResult, error) {
	result := AdapterResult{
		Adapter: qoderAdapterID, Detected: adapter.Detect(), HookPath: adapter.settingsPath(),
	}
	root, exists, err := readJSONObject(adapter.settingsPath())
	if err != nil {
		return result, err
	}
	command, args, err := adapter.command()
	if err != nil {
		return result, err
	}
	receipt, receiptErr := adapter.readReceipt()
	migrated := false
	if receiptErr == nil &&
		(receipt.Command != command || !equalStrings(receipt.Args, args)) {
		migrated, err = removeQoderHooks(root, receipt.Command, receipt.Args)
		if err != nil {
			return result, err
		}
	}
	changed, err := mergeQoderHooks(root, command, args)
	if err != nil {
		return result, err
	}
	changed = changed || migrated
	result.Installed = true
	result.Changed = changed
	if dryRun {
		result.Message = "AgentBell Qoder hooks would be installed for CLI, IDE and JetBrains sessions"
		return result, nil
	}
	if !changed {
		result.Message = "AgentBell Qoder hooks are already installed"
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
	receipt = qoderReceipt{
		Version:      1,
		Adapter:      qoderAdapterID,
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
	result.Message = "AgentBell Qoder hooks are installed for CLI, IDE and JetBrains sessions"
	return result, nil
}

func (adapter *QoderAdapter) Verify() (AdapterResult, error) {
	result := AdapterResult{
		Adapter: qoderAdapterID, Detected: adapter.Detect(), HookPath: adapter.settingsPath(),
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
	for _, eventName := range qoderHookEvents {
		if !hasQoderHook(root, eventName, command, args) {
			return result, fmt.Errorf("AgentBell Qoder hook for %s is missing", eventName)
		}
	}
	result.Installed = true
	result.Message = "AgentBell Qoder hooks are installed for CLI, IDE and JetBrains sessions"
	return result, nil
}

func (adapter *QoderAdapter) Uninstall(dryRun bool) (AdapterResult, error) {
	result := AdapterResult{
		Adapter: qoderAdapterID, Detected: adapter.Detect(), HookPath: adapter.settingsPath(),
	}
	root, exists, err := readJSONObject(adapter.settingsPath())
	if err != nil {
		return result, err
	}
	if !exists {
		result.Message = "Qoder settings file does not exist"
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
	changed, err := removeQoderHooks(root, command, args)
	if err != nil {
		return result, err
	}
	result.Changed = changed
	if dryRun {
		if changed {
			result.Message = "AgentBell Qoder hooks would be uninstalled"
		} else {
			result.Message = "AgentBell Qoder hooks are not installed"
		}
		return result, nil
	}
	if !changed {
		result.Message = "AgentBell Qoder hooks are not installed"
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
	result.Message = "AgentBell Qoder hooks are uninstalled"
	return result, nil
}

func (adapter *QoderAdapter) Diagnose() AdapterResult {
	result, err := adapter.Verify()
	if err != nil {
		result.Message = err.Error()
		return result
	}
	proof, verified := runtimeProofAfterConfig(
		adapter.StateDir,
		qoderAdapterID,
		adapter.settingsPath(),
	)
	result.RuntimeVerified = verified
	if !proof.LastSeen.IsZero() {
		result.LastSeen = proof.LastSeen.Format(time.RFC3339Nano)
	}
	if verified {
		result.Message = "AgentBell Qoder hooks have run since the last settings change"
	} else {
		result.Message = "Qoder hooks are installed but not yet observed after the last settings change; complete a new CLI or IDE turn"
	}
	return result
}

func (adapter *QoderAdapter) settingsPath() string {
	return filepath.Join(adapter.QoderHome, "settings.json")
}

func (adapter *QoderAdapter) command() (string, []string, error) {
	if strings.ContainsAny(adapter.Executable, "\x00\n\r") {
		return "", nil, errors.New("AgentBell executable path contains unsupported characters")
	}
	return adapter.Executable, []string{
		"emit",
		"--adapter", qoderAdapterID,
		"--surface", "cli",
		"--runtime", "host",
		"--stdin",
		"--fail-open",
	}, nil
}

func (adapter *QoderAdapter) backup(source string) (string, error) {
	value, err := os.ReadFile(source)
	if err != nil {
		return "", err
	}
	directory := filepath.Join(adapter.StateDir, "adapters", qoderAdapterID, "backups")
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

func (adapter *QoderAdapter) receiptPath() string {
	return filepath.Join(adapter.StateDir, "adapters", qoderAdapterID, "receipt.json")
}

func (adapter *QoderAdapter) writeReceipt(receipt qoderReceipt) error {
	return writeJSONFile(adapter.receiptPath(), receipt)
}

func (adapter *QoderAdapter) readReceipt() (qoderReceipt, error) {
	value, err := os.ReadFile(adapter.receiptPath())
	if err != nil {
		return qoderReceipt{}, err
	}
	var receipt qoderReceipt
	if err := json.Unmarshal(value, &receipt); err != nil {
		return qoderReceipt{}, err
	}
	if receipt.Version != 1 || receipt.Adapter != qoderAdapterID ||
		receipt.Command == "" || len(receipt.Args) == 0 {
		return qoderReceipt{}, errors.New("invalid Qoder adapter receipt")
	}
	return receipt, nil
}

func (adapter *QoderAdapter) now() time.Time {
	if adapter.Now != nil {
		return adapter.Now()
	}
	return time.Now()
}

func (adapter *QoderAdapter) lookPath() func(string) (string, error) {
	if adapter.LookPath != nil {
		return adapter.LookPath
	}
	return exec.LookPath
}

func mergeQoderHooks(root map[string]any, command string, args []string) (bool, error) {
	hooks, err := objectField(root, "hooks", true)
	if err != nil {
		return false, err
	}
	changed := false
	for _, eventName := range qoderHookEvents {
		if hasQoderHook(root, eventName, command, args) {
			continue
		}
		groups, err := arrayField(hooks, eventName, true)
		if err != nil {
			return false, err
		}
		group := map[string]any{
			"hooks": []any{qoderHandler(command, args)},
		}
		hooks[eventName] = append(groups, group)
		changed = true
	}
	return changed, nil
}

func removeQoderHooks(root map[string]any, command string, args []string) (bool, error) {
	hooks, err := objectField(root, "hooks", false)
	if err != nil {
		return false, err
	}
	if hooks == nil {
		return false, nil
	}
	changed := false
	for _, eventName := range qoderHookEvents {
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
				if ok && matchesQoderHandler(handler, command, args) {
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

func hasQoderHook(root map[string]any, eventName, command string, args []string) bool {
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
			if ok && matchesQoderHandler(handler, command, args) {
				return true
			}
		}
	}
	return false
}

func qoderHandler(command string, args []string) map[string]any {
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

func matchesQoderHandler(handler map[string]any, command string, args []string) bool {
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
