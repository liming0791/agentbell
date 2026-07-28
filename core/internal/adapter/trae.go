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

const traeAdapterID = "trae"

var traeHookSpecs = []shellHookSpec{
	{Event: "Notification", Matcher: "idle_prompt|permission_prompt"},
}

type TraeAdapter struct {
	Executable  string
	StateDir    string
	TraeHome    string
	GOOS        string
	Now         func() time.Time
	LookPath    func(string) (string, error)
	DetectPaths []string
}

type traeReceipt struct {
	Version     int       `json:"version"`
	Adapter     string    `json:"adapter"`
	HookPath    string    `json:"hookPath"`
	Command     string    `json:"command"`
	Backup      string    `json:"backup,omitempty"`
	InstalledAt time.Time `json:"installedAt"`
}

func NewTraeAdapter(executable, stateDir string) (*TraeAdapter, error) {
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
	configHome := os.Getenv("TRAE_CONFIG_DIR")
	pathExists := func(path string) bool {
		_, statErr := os.Stat(path)
		return statErr == nil
	}
	if configHome == "" {
		configHome = traeHome(userHome, runtime.GOOS, pathExists)
	}
	return &TraeAdapter{
		Executable:  absolute,
		StateDir:    stateDir,
		TraeHome:    configHome,
		GOOS:        runtime.GOOS,
		Now:         time.Now,
		LookPath:    exec.LookPath,
		DetectPaths: traeDetectPathsFor(userHome, runtime.GOOS),
	}, nil
}

func (adapter *TraeAdapter) Detect() bool {
	if _, err := adapter.lookPath()("trae"); err == nil {
		return true
	}
	for _, path := range append([]string{adapter.TraeHome}, adapter.DetectPaths...) {
		if path != "" {
			if _, err := os.Stat(path); err == nil {
				return true
			}
		}
	}
	return false
}

func (adapter *TraeAdapter) Plan() AdapterPlan {
	return AdapterPlan{
		Adapter:    traeAdapterID,
		Detected:   adapter.Detect(),
		HookPath:   adapter.hookPath(),
		Executable: adapter.Executable,
		Changes: []string{
			"merge one asynchronous Notification hook for idle_prompt and permission_prompt",
			"map idle_prompt to task.completed and permission_prompt to approval.required",
			"write an ownership receipt for precise uninstall",
			"enable configured Hooks in TRAE and select local automatic execution",
		},
	}
}

func (adapter *TraeAdapter) Install(dryRun bool) (AdapterResult, error) {
	result := AdapterResult{
		Adapter: traeAdapterID, Detected: adapter.Detect(), HookPath: adapter.hookPath(),
	}
	if err := adapter.validatePlatform(); err != nil {
		return result, err
	}
	root, exists, err := readJSONObject(adapter.hookPath())
	if err != nil {
		return result, err
	}
	command, err := adapter.command()
	if err != nil {
		return result, err
	}
	changed := false
	if receipt, receiptErr := adapter.readReceipt(); receiptErr == nil && receipt.Command != command {
		removed, removeErr := removeShellHooks(root, traeHookSpecs, receipt.Command)
		if removeErr != nil {
			return result, removeErr
		}
		changed = removed
	}
	removed, err := removeManagedShellHooks(
		root,
		traeHookSpecs,
		traeAdapterID,
		"ide",
		command,
	)
	if err != nil {
		return result, err
	}
	changed = changed || removed
	merged, err := mergeShellHooks(root, traeHookSpecs, command, true)
	if err != nil {
		return result, err
	}
	changed = changed || merged
	result.Installed = true
	result.Changed = changed
	if dryRun {
		result.Message = "AgentBell TRAE Notification hook would be installed"
		return result, nil
	}
	if !changed {
		result.Message = "AgentBell TRAE Notification hook is already installed"
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
	receipt := traeReceipt{
		Version:     1,
		Adapter:     traeAdapterID,
		HookPath:    adapter.hookPath(),
		Command:     command,
		Backup:      backup,
		InstalledAt: adapter.now().UTC(),
	}
	if err := adapter.writeReceipt(receipt); err != nil {
		return result, err
	}
	result.Backup = backup
	result.Message = "AgentBell TRAE Notification hook is installed; enable configured Hooks and select local automatic execution in TRAE"
	return result, nil
}

func (adapter *TraeAdapter) Verify() (AdapterResult, error) {
	result := AdapterResult{
		Adapter: traeAdapterID, Detected: adapter.Detect(), HookPath: adapter.hookPath(),
	}
	if err := adapter.validatePlatform(); err != nil {
		return result, err
	}
	root, _, err := readJSONObject(adapter.hookPath())
	if err != nil {
		return result, err
	}
	if root["version"] != float64(1) {
		return result, errorsForHookVersion(root["version"])
	}
	command, commandErr := adapter.command()
	receipt, receiptErr := adapter.readReceipt()
	if receiptErr == nil {
		command = receipt.Command
	}
	if commandErr != nil && receiptErr != nil {
		return result, errors.Join(commandErr, receiptErr)
	}
	for _, spec := range traeHookSpecs {
		if !hasShellHook(root, spec, command) {
			return result, fmt.Errorf("AgentBell TRAE hook for %s is missing", spec.Event)
		}
	}
	result.Installed = true
	result.Message = "AgentBell TRAE Notification hook is installed"
	return result, nil
}

func (adapter *TraeAdapter) Uninstall(dryRun bool) (AdapterResult, error) {
	result := AdapterResult{
		Adapter: traeAdapterID, Detected: adapter.Detect(), HookPath: adapter.hookPath(),
	}
	if err := adapter.validatePlatform(); err != nil {
		result.Message = err.Error()
		return result, nil
	}
	root, exists, err := readJSONObject(adapter.hookPath())
	if err != nil {
		return result, err
	}
	if !exists {
		result.Message = "TRAE hooks file does not exist"
		return result, nil
	}
	changed, err := removeManagedShellHooks(
		root,
		traeHookSpecs,
		traeAdapterID,
		"ide",
		"",
	)
	if err != nil {
		return result, err
	}
	result.Changed = changed
	if dryRun {
		if changed {
			result.Message = "AgentBell TRAE Notification hook would be uninstalled"
		} else {
			result.Message = "AgentBell TRAE Notification hook is not installed"
		}
		return result, nil
	}
	if !changed {
		result.Message = "AgentBell TRAE Notification hook is not installed"
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
	result.Message = "AgentBell TRAE Notification hook is uninstalled"
	return result, nil
}

func (adapter *TraeAdapter) Diagnose() AdapterResult {
	result, err := adapter.Verify()
	if err != nil {
		result.Message = err.Error()
		return result
	}
	proof, verified := runtimeEventProofAfterConfig(
		adapter.StateDir,
		traeAdapterID,
		"task.completed",
		adapter.hookPath(),
	)
	result.RuntimeVerified = verified
	if !proof.LastSeen.IsZero() {
		result.LastSeen = proof.LastSeen.Format(time.RFC3339Nano)
	}
	if verified {
		result.Message = "AgentBell TRAE hook has delivered task.completed since the last config change"
	} else {
		result.Message = "TRAE hook is installed but task.completed has not been observed; enable configured Hooks, select local automatic execution, and complete a new task"
	}
	return result
}

func (adapter *TraeAdapter) hookPath() string {
	return filepath.Join(adapter.TraeHome, "hooks.json")
}

func (adapter *TraeAdapter) command() (string, error) {
	if strings.ContainsAny(adapter.Executable, "\x00\n\r") {
		return "", errors.New("AgentBell executable path contains unsupported characters")
	}
	arguments := " emit --adapter trae --surface ide --runtime host --stdin --fail-open"
	if adapter.GOOS == "windows" {
		return "& " + powerShellQuote(adapter.Executable) + arguments, nil
	}
	return shellQuote(adapter.Executable) + arguments, nil
}

func (adapter *TraeAdapter) validatePlatform() error {
	if adapter.GOOS != "darwin" && adapter.GOOS != "windows" {
		return fmt.Errorf("TRAE adapter is not supported on %s", adapter.GOOS)
	}
	return nil
}

func (adapter *TraeAdapter) backup(source string) (string, error) {
	value, err := os.ReadFile(source)
	if err != nil {
		return "", err
	}
	directory := filepath.Join(adapter.StateDir, "adapters", traeAdapterID, "backups")
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

func (adapter *TraeAdapter) receiptPath() string {
	return filepath.Join(adapter.StateDir, "adapters", traeAdapterID, "receipt.json")
}

func (adapter *TraeAdapter) writeReceipt(receipt traeReceipt) error {
	return writeJSONFile(adapter.receiptPath(), receipt)
}

func (adapter *TraeAdapter) readReceipt() (traeReceipt, error) {
	value, err := os.ReadFile(adapter.receiptPath())
	if err != nil {
		return traeReceipt{}, err
	}
	var receipt traeReceipt
	if err := json.Unmarshal(value, &receipt); err != nil {
		return traeReceipt{}, err
	}
	if receipt.Version != 1 || receipt.Adapter != traeAdapterID || receipt.Command == "" {
		return traeReceipt{}, errors.New("invalid TRAE adapter receipt")
	}
	return receipt, nil
}

func (adapter *TraeAdapter) now() time.Time {
	if adapter.Now != nil {
		return adapter.Now()
	}
	return time.Now()
}

func (adapter *TraeAdapter) lookPath() func(string) (string, error) {
	if adapter.LookPath != nil {
		return adapter.LookPath
	}
	return exec.LookPath
}

func powerShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func traeDetectPaths(userHome string) []string {
	return traeDetectPathsFor(userHome, runtime.GOOS)
}

func traeDetectPathsFor(userHome, goos string) []string {
	switch goos {
	case "darwin":
		return []string{
			filepath.Join(userHome, "Applications", "TRAE.app"),
			filepath.Join(userHome, "Applications", "Trae CN.app"),
			filepath.Join(string(filepath.Separator), "Applications", "TRAE.app"),
			filepath.Join(string(filepath.Separator), "Applications", "Trae CN.app"),
		}
	case "windows":
		return []string{
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "TRAE", "TRAE.exe"),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "TRAE", "TRAE.exe"),
			filepath.Join(os.Getenv("ProgramFiles"), "TRAE", "TRAE.exe"),
		}
	default:
		return nil
	}
}

func traeHome(userHome, goos string, pathExists func(string) bool) string {
	internationalHome := filepath.Join(userHome, ".trae")
	cnHome := filepath.Join(userHome, ".trae-cn")
	if pathExists == nil {
		return internationalHome
	}
	if pathExists(cnHome) {
		return cnHome
	}
	if goos == "darwin" {
		for _, path := range []string{
			filepath.Join(userHome, "Applications", "Trae CN.app"),
			filepath.Join(string(filepath.Separator), "Applications", "Trae CN.app"),
		} {
			if pathExists(path) {
				return cnHome
			}
		}
	}
	return internationalHome
}
