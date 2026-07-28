package adapter

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const codexAdapterID = "codex"

const codexHooksDescription = "Lifecycle hooks including AgentBell notifications."

type CodexAdapter struct {
	Executable       string
	BridgeExecutable string
	ActiveGeneration uint64
	StateDir         string
	CodexHome        string
	Now              func() time.Time
	LookPath         func(string) (string, error)
}

type AdapterPlan struct {
	Adapter    string   `json:"adapter"`
	Detected   bool     `json:"detected"`
	HookPath   string   `json:"hookPath"`
	Executable string   `json:"executable"`
	Changes    []string `json:"changes"`
}

type AdapterResult struct {
	Adapter         string `json:"adapter"`
	Detected        bool   `json:"detected"`
	Installed       bool   `json:"installed"`
	RuntimeVerified bool   `json:"runtimeVerified"`
	Changed         bool   `json:"changed"`
	HookPath        string `json:"hookPath"`
	Backup          string `json:"backup,omitempty"`
	LastSeen        string `json:"lastSeen,omitempty"`
	Message         string `json:"message,omitempty"`
}

type codexReceipt struct {
	Version              int       `json:"version"`
	Adapter              string    `json:"adapter"`
	HookPath             string    `json:"hookPath"`
	Command              string    `json:"command"`
	CommandWindows       string    `json:"commandWindows"`
	Backup               string    `json:"backup,omitempty"`
	InstalledAt          time.Time `json:"installedAt"`
	BridgeProtocol       int       `json:"bridgeProtocol,omitempty"`
	ActivationGeneration uint64    `json:"activationGeneration,omitempty"`
}

func NewCodexAdapter(executable, stateDir string) (*CodexAdapter, error) {
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
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		home = filepath.Join(userHome, ".codex")
	}
	return &CodexAdapter{
		Executable: absolute,
		StateDir:   stateDir,
		CodexHome:  home,
		Now:        time.Now,
		LookPath:   exec.LookPath,
	}, nil
}

func (adapter *CodexAdapter) Detect() bool {
	if _, err := adapter.lookPath()("codex"); err == nil {
		return true
	}
	_, err := os.Stat(adapter.CodexHome)
	return err == nil
}

func (adapter *CodexAdapter) Plan() AdapterPlan {
	return AdapterPlan{
		Adapter:    codexAdapterID,
		Detected:   adapter.Detect(),
		HookPath:   adapter.hookPath(),
		Executable: plannedHookExecutable(adapter.Executable, adapter.BridgeExecutable),
		Changes: []string{
			"merge AgentBell command hook into Stop",
			"remove legacy ambiguous PermissionRequest notification hook",
			"write an ownership receipt for precise uninstall",
		},
	}
}

func (adapter *CodexAdapter) Install(dryRun bool) (AdapterResult, error) {
	result := AdapterResult{
		Adapter: codexAdapterID, Detected: adapter.Detect(), HookPath: adapter.hookPath(),
	}
	root, exists, err := readJSONObject(adapter.hookPath())
	if err != nil {
		return result, err
	}
	command, commandWindows, err := adapter.commands()
	if err != nil {
		return result, err
	}
	receipt, receiptErr := adapter.readReceipt()
	migrated := false
	if adapter.BridgeExecutable != "" &&
		receiptErr == nil &&
		(receipt.Command != command || receipt.CommandWindows != commandWindows) {
		migrated, err = removeCodexHooks(
			root,
			receipt.Command,
			receipt.CommandWindows,
		)
		if err != nil {
			return result, err
		}
	}
	changed, err := mergeCodexHooks(root, command, commandWindows)
	if err != nil {
		return result, err
	}
	changed = changed || migrated
	result.Installed = true
	result.Changed = changed
	if dryRun {
		result.Message = "AgentBell Codex completion hook would be installed; review it in Codex /hooks"
		return result, nil
	}
	if !changed {
		result.Message = "AgentBell Codex completion hook is already installed"
		return result, nil
	}

	if err := os.MkdirAll(filepath.Dir(adapter.hookPath()), 0o700); err != nil {
		return result, err
	}
	backup := ""
	if exists {
		backup, err = adapter.backup(adapter.hookPath())
		if err != nil {
			return result, err
		}
	}
	if err := writeJSONObject(adapter.hookPath(), root); err != nil {
		return result, err
	}
	invocation, err := adapter.hookInvocation()
	if err != nil {
		return result, err
	}
	receipt = codexReceipt{
		Version:              receiptVersion(invocation),
		Adapter:              codexAdapterID,
		HookPath:             adapter.hookPath(),
		Command:              command,
		CommandWindows:       commandWindows,
		Backup:               backup,
		InstalledAt:          adapter.now().UTC(),
		BridgeProtocol:       invocation.BridgeProtocol,
		ActivationGeneration: invocation.ActivationGeneration,
	}
	if err := adapter.writeReceipt(receipt); err != nil {
		return result, err
	}
	result.Backup = backup
	result.Message = "AgentBell Codex completion hook is installed; review and trust it in Codex /hooks, then start a new Codex task"
	return result, nil
}

func (adapter *CodexAdapter) Verify() (AdapterResult, error) {
	result := AdapterResult{
		Adapter: codexAdapterID, Detected: adapter.Detect(), HookPath: adapter.hookPath(),
	}
	root, _, err := readJSONObject(adapter.hookPath())
	if err != nil {
		return result, err
	}
	command, commandWindows, err := adapter.commands()
	if err != nil {
		return result, err
	}
	result.Installed = hasCodexHook(root, "Stop", command, commandWindows)
	if !result.Installed {
		return result, errors.New("AgentBell Codex completion hook is incomplete")
	}
	result.Message = "AgentBell Codex completion hook is installed"
	return result, nil
}

func (adapter *CodexAdapter) Uninstall(dryRun bool) (AdapterResult, error) {
	result := AdapterResult{
		Adapter: codexAdapterID, Detected: adapter.Detect(), HookPath: adapter.hookPath(),
	}
	root, exists, err := readJSONObject(adapter.hookPath())
	if err != nil {
		return result, err
	}
	if !exists {
		result.Message = "Codex hooks file does not exist"
		return result, nil
	}

	receipt, receiptErr := adapter.readReceipt()
	command, commandWindows, commandErr := adapter.commands()
	if receiptErr == nil {
		command = receipt.Command
		commandWindows = receipt.CommandWindows
	}
	if commandErr != nil && receiptErr != nil {
		return result, errors.Join(commandErr, receiptErr)
	}
	changed, err := removeCodexHooks(root, command, commandWindows)
	if err != nil {
		return result, err
	}
	if root["description"] == codexHooksDescription {
		delete(root, "description")
		changed = true
	}
	result.Changed = changed
	result.Installed = false
	if dryRun {
		if changed {
			result.Message = "AgentBell Codex hooks would be uninstalled"
		} else {
			result.Message = "AgentBell Codex hooks are not installed"
		}
		return result, nil
	}
	if !changed {
		result.Message = "AgentBell Codex hooks are not installed"
		return result, nil
	}

	backup, err := adapter.backup(adapter.hookPath())
	if err != nil {
		return result, err
	}
	if err := writeJSONObject(adapter.hookPath(), root); err != nil {
		return result, err
	}
	_ = os.Remove(adapter.receiptPath())
	result.Backup = backup
	result.Message = "AgentBell Codex hooks are uninstalled"
	return result, nil
}

func (adapter *CodexAdapter) Diagnose() AdapterResult {
	result, err := adapter.Verify()
	if err != nil {
		result.Message = err.Error()
		return result
	}
	receipt, receiptErr := adapter.readReceipt()
	var proof runtimeProof
	var verified bool
	if receiptErr == nil && bridgeReceiptActive(
		receipt.Version,
		receipt.BridgeProtocol,
		receipt.ActivationGeneration,
	) {
		proof, verified = runtimeEventProofAfterConfigAndGeneration(
			adapter.StateDir,
			codexAdapterID,
			"task.completed",
			adapter.hookPath(),
			adapter.ActiveGeneration,
		)
	} else {
		proof, verified = runtimeEventProofAfterConfig(
			adapter.StateDir,
			codexAdapterID,
			"task.completed",
			adapter.hookPath(),
		)
	}
	result.RuntimeVerified = verified
	if !proof.LastSeen.IsZero() {
		result.LastSeen = proof.LastSeen.Format(time.RFC3339Nano)
	}
	if verified {
		result.Message = "AgentBell Codex Stop hook has delivered task.completed since the last config change"
	} else {
		result.Message = "Codex Stop hook is installed but task.completed has not been observed after the last config change; review and trust it in Codex /hooks, then start and complete a new Codex task"
	}
	return result
}

func (adapter *CodexAdapter) hookPath() string {
	return filepath.Join(adapter.CodexHome, "hooks.json")
}

func (adapter *CodexAdapter) commands() (string, string, error) {
	invocation, err := adapter.hookInvocation()
	if err != nil {
		return "", "", err
	}
	return invocation.shellCommand(false), invocation.shellCommand(true), nil
}

func (adapter *CodexAdapter) hookInvocation() (hookInvocation, error) {
	return resolveHookInvocation(
		adapter.Executable,
		adapter.BridgeExecutable,
		adapter.ActiveGeneration,
		codexAdapterID,
		"cli",
		"host",
	)
}

func (adapter *CodexAdapter) backup(source string) (string, error) {
	value, err := os.ReadFile(source)
	if err != nil {
		return "", err
	}
	directory := filepath.Join(adapter.StateDir, "adapters", codexAdapterID, "backups")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	name := fmt.Sprintf(
		"hooks-%s-%s.json",
		adapter.now().UTC().Format("20060102T150405.000000000Z"),
		hashBytes(value)[:12],
	)
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, value, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func (adapter *CodexAdapter) receiptPath() string {
	return filepath.Join(adapter.StateDir, "adapters", codexAdapterID, "receipt.json")
}

func (adapter *CodexAdapter) writeReceipt(receipt codexReceipt) error {
	return writeJSONFile(adapter.receiptPath(), receipt)
}

func (adapter *CodexAdapter) readReceipt() (codexReceipt, error) {
	value, err := os.ReadFile(adapter.receiptPath())
	if err != nil {
		return codexReceipt{}, err
	}
	var receipt codexReceipt
	if err := json.Unmarshal(value, &receipt); err != nil {
		return codexReceipt{}, err
	}
	if receipt.Adapter != codexAdapterID ||
		validateReceiptBridge(
			receipt.Version,
			receipt.BridgeProtocol,
			receipt.ActivationGeneration,
		) != nil {
		return codexReceipt{}, errors.New("invalid Codex adapter receipt")
	}
	return receipt, nil
}

func (adapter *CodexAdapter) now() time.Time {
	if adapter.Now != nil {
		return adapter.Now()
	}
	return time.Now()
}

func (adapter *CodexAdapter) lookPath() func(string) (string, error) {
	if adapter.LookPath != nil {
		return adapter.LookPath
	}
	return exec.LookPath
}

func mergeCodexHooks(root map[string]any, command, commandWindows string) (bool, error) {
	hooks, err := objectField(root, "hooks", true)
	if err != nil {
		return false, err
	}
	changed := false
	for _, eventName := range []string{"Stop"} {
		if hasCodexHook(root, eventName, command, commandWindows) {
			continue
		}
		groups, err := arrayField(hooks, eventName, true)
		if err != nil {
			return false, err
		}
		handler := map[string]any{
			"type":           "command",
			"command":        command,
			"commandWindows": commandWindows,
			"timeout":        float64(5),
			"statusMessage":  "Queueing AgentBell notification",
		}
		groups = append(groups, map[string]any{"hooks": []any{handler}})
		hooks[eventName] = groups
		changed = true
	}
	removedLegacy, err := removeCodexHookEvents(
		root,
		command,
		commandWindows,
		[]string{"PermissionRequest"},
	)
	if err != nil {
		return false, err
	}
	changed = changed || removedLegacy
	// Codex 严格解析 hooks.json，顶层未知字段会导致整个文件被忽略；
	// 清除早期版本写入的 description，让重复安装自愈。
	if root["description"] == codexHooksDescription {
		delete(root, "description")
		changed = true
	}
	return changed, nil
}

func removeCodexHooks(root map[string]any, command, commandWindows string) (bool, error) {
	return removeCodexHookEvents(
		root,
		command,
		commandWindows,
		[]string{"Stop", "PermissionRequest"},
	)
}

func removeCodexHookEvents(
	root map[string]any,
	command, commandWindows string,
	eventNames []string,
) (bool, error) {
	hooks, err := objectField(root, "hooks", false)
	if err != nil {
		return false, err
	}
	if hooks == nil {
		return false, nil
	}
	changed := false
	for _, eventName := range eventNames {
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
				if ok && matchesHandler(handler, command, commandWindows) {
					changed = true
					continue
				}
				filteredHandlers = append(filteredHandlers, handlerValue)
			}
			if len(filteredHandlers) == 0 {
				if len(handlers) > 0 {
					continue
				}
			} else {
				group["hooks"] = filteredHandlers
			}
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

func hasCodexHook(root map[string]any, eventName, command, commandWindows string) bool {
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
			if ok && matchesHandler(handler, command, commandWindows) {
				return true
			}
		}
	}
	return false
}

func matchesHandler(handler map[string]any, command, commandWindows string) bool {
	return handler["type"] == "command" &&
		handler["command"] == command &&
		handler["commandWindows"] == commandWindows
}

func objectField(parent map[string]any, key string, create bool) (map[string]any, error) {
	value, ok := parent[key]
	if !ok {
		if !create {
			return nil, nil
		}
		result := make(map[string]any)
		parent[key] = result
		return result, nil
	}
	result, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an object", key)
	}
	return result, nil
}

func arrayField(parent map[string]any, key string, create bool) ([]any, error) {
	value, ok := parent[key]
	if !ok {
		if !create {
			return nil, nil
		}
		result := make([]any, 0)
		parent[key] = result
		return result, nil
	}
	result, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array", key)
	}
	return result, nil
}

func readJSONObject(path string) (map[string]any, bool, error) {
	value, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return make(map[string]any), false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var result map[string]any
	if err := json.Unmarshal(value, &result); err != nil {
		return nil, false, fmt.Errorf("parse %s: %w", path, err)
	}
	if result == nil {
		return nil, false, errors.New("hook JSON root must be an object")
	}
	return result, true, nil
}

func writeJSONObject(path string, value map[string]any) error {
	return writeJSONFile(path, value)
}

func writeJSONFile(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	return writeFileAtomic(path, encoded)
}

func writeFileAtomic(path string, encoded []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".agentbell-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(encoded); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func hashBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
