package adapter

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const qoderWorkAdapterID = "qoder-work"

var qoderWorkHookSpecs = []shellHookSpec{
	{Event: "Stop"},
	{Event: "PostToolUseFailure"},
	{Event: "PermissionRequest"},
}

type QoderWorkAdapter struct {
	Executable    string
	StateDir      string
	QoderWorkHome string
	GOOS          string
	Now           func() time.Time
	LookPath      func(string) (string, error)
	DetectPaths   []string
}

type qoderWorkReceipt struct {
	Version      int       `json:"version"`
	Adapter      string    `json:"adapter"`
	SettingsPath string    `json:"settingsPath"`
	Command      string    `json:"command"`
	Backup       string    `json:"backup,omitempty"`
	InstalledAt  time.Time `json:"installedAt"`
}

func NewQoderWorkAdapter(executable, stateDir string) (*QoderWorkAdapter, error) {
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
	userHome, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	configHome := os.Getenv("QODERWORK_CONFIG_DIR")
	pathExists := func(path string) bool {
		_, statErr := os.Stat(path)
		return statErr == nil
	}
	if configHome == "" {
		configHome = qoderWorkHome(userHome, runtime.GOOS, pathExists)
	}
	return &QoderWorkAdapter{
		Executable:    absolute,
		StateDir:      stateDir,
		QoderWorkHome: configHome,
		GOOS:          runtime.GOOS,
		Now:           time.Now,
		LookPath:      exec.LookPath,
		DetectPaths:   qoderWorkDetectPathsFor(userHome, runtime.GOOS),
	}, nil
}

func (adapter *QoderWorkAdapter) Detect() bool {
	if _, err := adapter.lookPath()("qoderwork"); err == nil {
		return true
	}
	for _, path := range append([]string{adapter.QoderWorkHome}, adapter.DetectPaths...) {
		if path != "" {
			if _, err := os.Stat(path); err == nil {
				return true
			}
		}
	}
	return false
}

func (adapter *QoderWorkAdapter) Plan() AdapterPlan {
	return AdapterPlan{
		Adapter:    qoderWorkAdapterID,
		Detected:   adapter.Detect(),
		HookPath:   adapter.settingsPath(),
		Executable: adapter.Executable,
		Changes: []string{
			"merge AgentBell shell-command hooks into Stop, PostToolUseFailure and PermissionRequest",
			"write only the independent QoderWork user settings profile",
			"write an ownership receipt for precise uninstall",
			"restart QoderWork after the settings change because hooks are not hot-reloaded",
		},
	}
}

func (adapter *QoderWorkAdapter) Install(dryRun bool) (AdapterResult, error) {
	result := AdapterResult{
		Adapter: qoderWorkAdapterID, Detected: adapter.Detect(), HookPath: adapter.settingsPath(),
	}
	if err := adapter.validatePlatform(); err != nil {
		return result, err
	}
	root, exists, err := readJSONObject(adapter.settingsPath())
	if err != nil {
		return result, err
	}
	command, err := adapter.command()
	if err != nil {
		return result, err
	}
	changed := false
	if receipt, receiptErr := adapter.readReceipt(); receiptErr == nil && receipt.Command != command {
		removed, removeErr := removeShellHooks(root, qoderWorkHookSpecs, receipt.Command)
		if removeErr != nil {
			return result, removeErr
		}
		changed = removed
	}
	removed, err := removeManagedShellHooks(
		root,
		qoderWorkHookSpecs,
		qoderWorkAdapterID,
		"desktop",
		command,
	)
	if err != nil {
		return result, err
	}
	changed = changed || removed
	merged, err := mergeShellHooks(root, qoderWorkHookSpecs, command, false)
	if err != nil {
		return result, err
	}
	changed = changed || merged
	result.Installed = true
	result.Changed = changed
	if dryRun {
		result.Message = "AgentBell QoderWork hooks would be installed; restart QoderWork afterward"
		return result, nil
	}
	if !changed {
		result.Message = "AgentBell QoderWork hooks are already installed"
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
	receipt := qoderWorkReceipt{
		Version:      1,
		Adapter:      qoderWorkAdapterID,
		SettingsPath: adapter.settingsPath(),
		Command:      command,
		Backup:       backup,
		InstalledAt:  adapter.now().UTC(),
	}
	if err := adapter.writeReceipt(receipt); err != nil {
		return result, err
	}
	result.Backup = backup
	result.Message = "AgentBell QoderWork hooks are installed; restart QoderWork to load them"
	return result, nil
}

func (adapter *QoderWorkAdapter) Verify() (AdapterResult, error) {
	result := AdapterResult{
		Adapter: qoderWorkAdapterID, Detected: adapter.Detect(), HookPath: adapter.settingsPath(),
	}
	if err := adapter.validatePlatform(); err != nil {
		return result, err
	}
	root, _, err := readJSONObject(adapter.settingsPath())
	if err != nil {
		return result, err
	}
	command, commandErr := adapter.command()
	receipt, receiptErr := adapter.readReceipt()
	if receiptErr == nil {
		command = receipt.Command
	}
	if commandErr != nil && receiptErr != nil {
		return result, errors.Join(commandErr, receiptErr)
	}
	for _, spec := range qoderWorkHookSpecs {
		if !hasShellHook(root, spec, command) {
			return result, fmt.Errorf("AgentBell QoderWork hook for %s is missing", spec.Event)
		}
	}
	result.Installed = true
	result.Message = "AgentBell QoderWork hooks are installed"
	return result, nil
}

func (adapter *QoderWorkAdapter) Uninstall(dryRun bool) (AdapterResult, error) {
	result := AdapterResult{
		Adapter: qoderWorkAdapterID, Detected: adapter.Detect(), HookPath: adapter.settingsPath(),
	}
	if err := adapter.validatePlatform(); err != nil {
		result.Message = err.Error()
		return result, nil
	}
	root, exists, err := readJSONObject(adapter.settingsPath())
	if err != nil {
		return result, err
	}
	if !exists {
		result.Message = "QoderWork settings file does not exist"
		return result, nil
	}
	changed, err := removeManagedShellHooks(
		root,
		qoderWorkHookSpecs,
		qoderWorkAdapterID,
		"desktop",
		"",
	)
	if err != nil {
		return result, err
	}
	result.Changed = changed
	if dryRun {
		if changed {
			result.Message = "AgentBell QoderWork hooks would be uninstalled"
		} else {
			result.Message = "AgentBell QoderWork hooks are not installed"
		}
		return result, nil
	}
	if !changed {
		result.Message = "AgentBell QoderWork hooks are not installed"
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
	result.Message = "AgentBell QoderWork hooks are uninstalled; restart QoderWork"
	return result, nil
}

func (adapter *QoderWorkAdapter) Diagnose() AdapterResult {
	result, err := adapter.Verify()
	if err != nil {
		result.Message = err.Error()
		return result
	}
	proof, verified := runtimeProofAfterConfig(
		adapter.StateDir,
		qoderWorkAdapterID,
		adapter.settingsPath(),
	)
	result.RuntimeVerified = verified
	if !proof.LastSeen.IsZero() {
		result.LastSeen = proof.LastSeen.Format(time.RFC3339Nano)
	}
	if verified {
		result.Message = "AgentBell QoderWork hooks have run since the last settings change"
	} else {
		result.Message = "QoderWork hooks are installed but not yet observed after the last settings change; restart QoderWork and complete a new task"
	}
	return result
}

func (adapter *QoderWorkAdapter) settingsPath() string {
	return filepath.Join(adapter.QoderWorkHome, "settings.json")
}

func (adapter *QoderWorkAdapter) command() (string, error) {
	if strings.ContainsAny(adapter.Executable, "\x00\n\r") {
		return "", errors.New("AgentBell executable path contains unsupported characters")
	}
	arguments := " emit --adapter qoder-work --surface desktop --runtime host --stdin --fail-open"
	if adapter.GOOS == "windows" {
		if strings.Contains(adapter.Executable, `"`) {
			return "", errors.New("AgentBell executable path contains unsupported characters")
		}
		return `"` + adapter.Executable + `"` + arguments, nil
	}
	return shellQuote(adapter.Executable) + arguments, nil
}

func (adapter *QoderWorkAdapter) validatePlatform() error {
	if adapter.GOOS != "darwin" && adapter.GOOS != "windows" {
		return fmt.Errorf("QoderWork adapter is not supported on %s", adapter.GOOS)
	}
	return nil
}

func (adapter *QoderWorkAdapter) backup(source string) (string, error) {
	value, err := os.ReadFile(source)
	if err != nil {
		return "", err
	}
	directory := filepath.Join(adapter.StateDir, "adapters", qoderWorkAdapterID, "backups")
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

func (adapter *QoderWorkAdapter) receiptPath() string {
	return filepath.Join(adapter.StateDir, "adapters", qoderWorkAdapterID, "receipt.json")
}

func (adapter *QoderWorkAdapter) writeReceipt(receipt qoderWorkReceipt) error {
	return writeJSONFile(adapter.receiptPath(), receipt)
}

func (adapter *QoderWorkAdapter) readReceipt() (qoderWorkReceipt, error) {
	value, err := os.ReadFile(adapter.receiptPath())
	if err != nil {
		return qoderWorkReceipt{}, err
	}
	var receipt qoderWorkReceipt
	if err := json.Unmarshal(value, &receipt); err != nil {
		return qoderWorkReceipt{}, err
	}
	if receipt.Version != 1 || receipt.Adapter != qoderWorkAdapterID || receipt.Command == "" {
		return qoderWorkReceipt{}, errors.New("invalid QoderWork adapter receipt")
	}
	return receipt, nil
}

func (adapter *QoderWorkAdapter) now() time.Time {
	if adapter.Now != nil {
		return adapter.Now()
	}
	return time.Now()
}

func (adapter *QoderWorkAdapter) lookPath() func(string) (string, error) {
	if adapter.LookPath != nil {
		return adapter.LookPath
	}
	return exec.LookPath
}

func qoderWorkDetectPaths(userHome string) []string {
	return qoderWorkDetectPathsFor(userHome, runtime.GOOS)
}

func qoderWorkDetectPathsFor(userHome, goos string) []string {
	switch goos {
	case "darwin":
		return []string{
			filepath.Join(userHome, "Applications", "QoderWork.app"),
			filepath.Join(userHome, "Applications", "QoderWork CN.app"),
			filepath.Join(string(filepath.Separator), "Applications", "QoderWork.app"),
			filepath.Join(string(filepath.Separator), "Applications", "QoderWork CN.app"),
		}
	case "windows":
		return []string{
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "QoderWork", "QoderWork.exe"),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "QoderWork", "QoderWork.exe"),
			filepath.Join(os.Getenv("ProgramFiles"), "QoderWork", "QoderWork.exe"),
		}
	default:
		return nil
	}
}

func qoderWorkHome(userHome, goos string, pathExists func(string) bool) string {
	internationalHome := filepath.Join(userHome, ".qoderwork")
	cnHome := filepath.Join(userHome, ".qoderworkcn")
	if pathExists == nil {
		return internationalHome
	}
	if pathExists(cnHome) {
		return cnHome
	}
	if goos == "darwin" {
		for _, path := range []string{
			filepath.Join(userHome, "Applications", "QoderWork CN.app"),
			filepath.Join(string(filepath.Separator), "Applications", "QoderWork CN.app"),
		} {
			if pathExists(path) {
				return cnHome
			}
		}
	}
	return internationalHome
}
