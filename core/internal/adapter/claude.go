package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

const claudeAdapterID = "claude-code"

const (
	claudeHookFormExec  = "exec"
	claudeHookFormShell = "shell"
)

var claudeHookEvents = []string{"Stop", "StopFailure", "Notification", "PermissionRequest"}
var claudeBaselineHookEvents = []string{"Stop", "Notification"}

type ClaudeAdapter struct {
	Executable       string
	BridgeExecutable string
	ActiveGeneration uint64
	StateDir         string
	ClaudeHome       string
	Now              func() time.Time
	LookPath         func(string) (string, error)
	GOOS             string
	VersionOutput    func(string) (string, error)
}

type claudeReceipt struct {
	Version              int       `json:"version"`
	Adapter              string    `json:"adapter"`
	SettingsPath         string    `json:"settingsPath"`
	Command              string    `json:"command"`
	Args                 []string  `json:"args"`
	Backup               string    `json:"backup,omitempty"`
	InstalledAt          time.Time `json:"installedAt"`
	BridgeProtocol       int       `json:"bridgeProtocol,omitempty"`
	ActivationGeneration uint64    `json:"activationGeneration,omitempty"`
	HookForm             string    `json:"hookForm,omitempty"`
	ClaudeVersion        string    `json:"claudeVersion,omitempty"`
	VersionStatus        string    `json:"versionStatus,omitempty"`
	InvocationCommand    string    `json:"invocationCommand,omitempty"`
	InvocationArgs       []string  `json:"invocationArgs,omitempty"`
	Events               []string  `json:"events,omitempty"`
}

type claudeHookCommand struct {
	Command       string
	Args          []string
	Form          string
	ClaudeVersion string
	VersionKnown  bool
	Invocation    hookInvocation
	Events        []string
}

type claudeVersion struct {
	major      int
	minor      int
	patch      int
	prerelease bool
}

var claudeVersionPattern = regexp.MustCompile(
	`(?:^|[^0-9A-Za-z])v?([0-9]+)\.([0-9]+)\.([0-9]+)([-+][0-9A-Za-z.-]+)?(?:$|[^0-9A-Za-z.-])`,
)

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
		GOOS:       runtime.GOOS,
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
		Executable: plannedHookExecutable(adapter.Executable, adapter.BridgeExecutable),
		Changes: []string{
			"select safe exec-form hooks for Claude Code 2.1.139+ or a quoted compatibility command for older and unknown versions",
			"negotiate the supported Hook event set for the detected Claude Code version",
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
	selected, err := adapter.commandDetails()
	if err != nil {
		return result, err
	}
	receipt, receiptErr := adapter.readReceipt()
	migrated := false
	if receiptErr == nil &&
		(receipt.Command != selected.Command ||
			!equalStrings(receipt.Args, selected.Args) ||
			!equalStrings(receipt.Events, selected.Events)) {
		migrated, err = removeClaudeHooks(
			root,
			receipt.Events,
			receipt.Command,
			receipt.Args,
		)
		if err != nil {
			return result, err
		}
	} else if receiptErr != nil {
		alternate, alternateErr := adapter.alternateCommand(selected)
		if alternateErr == nil &&
			(alternate.Command != selected.Command ||
				!equalStrings(alternate.Args, selected.Args)) {
			migrated, err = removeClaudeHooks(
				root,
				claudeHookEvents,
				alternate.Command,
				alternate.Args,
			)
			if err != nil {
				return result, err
			}
		}
		removedUnsupported, removeErr := removeClaudeHooks(
			root,
			claudeUnsupportedEvents(selected.Events),
			selected.Command,
			selected.Args,
		)
		if removeErr != nil {
			return result, removeErr
		}
		migrated = migrated || removedUnsupported
	}
	changed, err := mergeClaudeHooks(
		root,
		selected.Events,
		selected.Command,
		selected.Args,
	)
	if err != nil {
		return result, err
	}
	changed = changed || migrated
	result.Installed = true
	result.Changed = changed
	if dryRun {
		result.Message = adapter.claudeInstallMessage(
			"AgentBell Claude Code hooks would be installed for CLI and Desktop local sessions",
			selected,
		)
		return result, nil
	}
	if !changed {
		result.Message = adapter.claudeInstallMessage(
			"AgentBell Claude Code hooks are already installed",
			selected,
		)
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
	receipt = claudeReceipt{
		Version:              receiptVersion(selected.Invocation),
		Adapter:              claudeAdapterID,
		SettingsPath:         adapter.settingsPath(),
		Command:              selected.Command,
		Args:                 selected.Args,
		Backup:               backup,
		InstalledAt:          adapter.now().UTC(),
		BridgeProtocol:       selected.Invocation.BridgeProtocol,
		ActivationGeneration: selected.Invocation.ActivationGeneration,
		HookForm:             selected.Form,
		ClaudeVersion:        selected.ClaudeVersion,
		VersionStatus:        claudeVersionStatus(selected.VersionKnown),
		InvocationCommand:    selected.Invocation.Executable,
		InvocationArgs:       append([]string(nil), selected.Invocation.Args...),
		Events:               append([]string(nil), selected.Events...),
	}
	if err := adapter.writeReceipt(receipt); err != nil {
		return result, err
	}
	result.Backup = backup
	result.Message = adapter.claudeInstallMessage(
		"AgentBell Claude Code hooks are installed for CLI and Desktop local sessions",
		selected,
	)
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
	selected, commandErr := adapter.commandDetails()
	command := selected.Command
	args := selected.Args
	receipt, receiptErr := adapter.readReceipt()
	events := selected.Events
	if receiptErr == nil {
		if commandErr == nil &&
			selected.Form == claudeHookFormShell &&
			receipt.HookForm == claudeHookFormExec {
			return result, errors.New(
				"installed Claude Code exec-form hooks are incompatible with the detected or unknown Claude Code version; rerun adapter install to migrate them",
			)
		}
		if commandErr == nil &&
			!equalStrings(receipt.Events, selected.Events) {
			return result, fmt.Errorf(
				"installed Claude Code Hook event set %v is incompatible with the supported event set %v for the detected or unknown Claude Code version; rerun adapter install to migrate it",
				receipt.Events,
				selected.Events,
			)
		}
		command = receipt.Command
		args = receipt.Args
		events = receipt.Events
	}
	if commandErr != nil && receiptErr != nil {
		return result, errors.Join(commandErr, receiptErr)
	}
	for _, eventName := range events {
		if !hasClaudeHook(root, eventName, command, args) {
			message := fmt.Sprintf(
				"AgentBell Claude Code hook for %s is missing",
				eventName,
			)
			if commandErr == nil {
				message += claudeCompatibilityNote(selected)
			}
			return result, errors.New(message)
		}
	}
	result.Installed = true
	result.Message = "AgentBell Claude Code hooks are installed for CLI and Desktop local sessions"
	if receiptErr == nil {
		result.Message += claudeReceiptCompatibilityNote(receipt)
	} else if commandErr == nil {
		result.Message += claudeCompatibilityNote(selected)
	}
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
	selected, commandErr := adapter.commandDetails()
	command := selected.Command
	args := selected.Args
	events := selected.Events
	receipt, receiptErr := adapter.readReceipt()
	if receiptErr == nil {
		command = receipt.Command
		args = receipt.Args
		events = receipt.Events
	}
	if commandErr != nil && receiptErr != nil {
		return result, errors.Join(commandErr, receiptErr)
	}
	changed, err := removeClaudeHooks(root, events, command, args)
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
	receipt, receiptErr := adapter.readReceipt()
	var proof runtimeProof
	var verified bool
	if receiptErr == nil && bridgeReceiptActive(
		receipt.Version,
		receipt.BridgeProtocol,
		receipt.ActivationGeneration,
	) {
		proof, verified = runtimeProofAfterConfigAndGeneration(
			adapter.StateDir,
			claudeAdapterID,
			adapter.settingsPath(),
			adapter.ActiveGeneration,
		)
	} else {
		proof, verified = runtimeProofAfterConfig(
			adapter.StateDir,
			claudeAdapterID,
			adapter.settingsPath(),
		)
	}
	result.RuntimeVerified = verified
	if !proof.LastSeen.IsZero() {
		result.LastSeen = proof.LastSeen.Format(time.RFC3339Nano)
	}
	if verified {
		result.Message = "AgentBell Claude Code hooks have run since the last settings change"
	} else {
		result.Message = "Claude Code hooks are installed but not yet observed after the last settings change; complete a new CLI or Desktop local turn"
	}
	if receiptErr == nil {
		result.Message += claudeReceiptCompatibilityNote(receipt)
	}
	return result
}

func (adapter *ClaudeAdapter) settingsPath() string {
	return filepath.Join(adapter.ClaudeHome, "settings.json")
}

func (adapter *ClaudeAdapter) command() (string, []string, error) {
	selected, err := adapter.commandDetails()
	if err != nil {
		return "", nil, err
	}
	return selected.Command, selected.Args, nil
}

func (adapter *ClaudeAdapter) hookInvocation() (hookInvocation, error) {
	return resolveHookInvocation(
		adapter.Executable,
		adapter.BridgeExecutable,
		adapter.ActiveGeneration,
		claudeAdapterID,
		"cli",
		"host",
	)
}

func (adapter *ClaudeAdapter) commandDetails() (claudeHookCommand, error) {
	invocation, err := adapter.hookInvocation()
	if err != nil {
		return claudeHookCommand{}, err
	}
	version, versionText, known := adapter.claudeVersion()
	selected := claudeHookCommand{
		Command:       invocation.Executable,
		Args:          append([]string(nil), invocation.Args...),
		Form:          claudeHookFormExec,
		ClaudeVersion: versionText,
		VersionKnown:  known,
		Invocation:    invocation,
		Events:        claudeEventsForVersion(version, known),
	}
	if known && version.supportsArgs() {
		return selected, nil
	}
	command, err := legacyClaudeCommand(invocation, adapter.goos())
	if err != nil {
		return claudeHookCommand{}, err
	}
	selected.Command = command
	selected.Args = nil
	selected.Form = claudeHookFormShell
	return selected, nil
}

func (adapter *ClaudeAdapter) alternateCommand(
	selected claudeHookCommand,
) (claudeHookCommand, error) {
	alternate := selected
	if selected.Form == claudeHookFormShell {
		alternate.Command = selected.Invocation.Executable
		alternate.Args = append([]string(nil), selected.Invocation.Args...)
		alternate.Form = claudeHookFormExec
		return alternate, nil
	}
	command, err := legacyClaudeCommand(selected.Invocation, adapter.goos())
	if err != nil {
		return claudeHookCommand{}, err
	}
	alternate.Command = command
	alternate.Args = nil
	alternate.Form = claudeHookFormShell
	return alternate, nil
}

func (adapter *ClaudeAdapter) claudeVersion() (claudeVersion, string, bool) {
	path, err := adapter.lookPath()("claude")
	if err != nil {
		return claudeVersion{}, "", false
	}
	output, err := adapter.versionOutput()(path)
	if err != nil {
		return claudeVersion{}, "", false
	}
	return parseClaudeVersion(output)
}

func parseClaudeVersion(value string) (claudeVersion, string, bool) {
	match := claudeVersionPattern.FindStringSubmatch(value)
	if len(match) == 0 {
		return claudeVersion{}, "", false
	}
	major, majorErr := strconv.Atoi(match[1])
	minor, minorErr := strconv.Atoi(match[2])
	patch, patchErr := strconv.Atoi(match[3])
	if majorErr != nil || minorErr != nil || patchErr != nil {
		return claudeVersion{}, "", false
	}
	suffix := match[4]
	return claudeVersion{
			major:      major,
			minor:      minor,
			patch:      patch,
			prerelease: strings.HasPrefix(suffix, "-"),
		},
		match[1] + "." + match[2] + "." + match[3] + suffix,
		true
}

func (value claudeVersion) supportsArgs() bool {
	return value.atLeast(2, 1, 139)
}

func (value claudeVersion) atLeast(major, minor, patch int) bool {
	switch {
	case value.major != major:
		return value.major > major
	case value.minor != minor:
		return value.minor > minor
	case value.patch != patch:
		return value.patch > patch
	default:
		return !value.prerelease
	}
}

func claudeEventsForVersion(
	version claudeVersion,
	known bool,
) []string {
	if !known {
		return append([]string(nil), claudeBaselineHookEvents...)
	}
	result := make([]string, 0, len(claudeHookEvents))
	for _, eventName := range claudeHookEvents {
		switch eventName {
		case "PermissionRequest":
			// Claude Code's official changelog introduced this event in
			// 2.0.45. Older clients reject the unknown event name and may
			// discard the entire hooks object.
			if !version.atLeast(2, 0, 45) {
				continue
			}
		case "StopFailure":
			// Claude Code's official changelog introduced this event in
			// 2.1.78.
			if !version.atLeast(2, 1, 78) {
				continue
			}
		}
		result = append(result, eventName)
	}
	return result
}

func claudeUnsupportedEvents(supported []string) []string {
	result := make([]string, 0, len(claudeHookEvents))
	for _, eventName := range claudeHookEvents {
		if !slices.Contains(supported, eventName) {
			result = append(result, eventName)
		}
	}
	return result
}

func legacyClaudeCommand(invocation hookInvocation, goos string) (string, error) {
	if goos == "windows" {
		if strings.ContainsAny(
			invocation.Executable,
			"%!&|<>^()$`;\x00\r\n\"",
		) {
			return "", errors.New(
				"AgentBell executable path is unsafe for a legacy Windows Claude Hook",
			)
		}
		return invocation.shellCommand(true), nil
	}
	if goos != "darwin" && goos != "linux" {
		return "", fmt.Errorf("Claude Code hooks are not supported on %s", goos)
	}
	if strings.ContainsAny(invocation.Executable, "\x00\r\n") {
		return "", errors.New(
			"AgentBell executable path is unsafe for a legacy POSIX Claude Hook",
		)
	}
	return invocation.shellCommand(false), nil
}

func (adapter *ClaudeAdapter) versionOutput() func(string) (string, error) {
	if adapter.VersionOutput != nil {
		return adapter.VersionOutput
	}
	return readClaudeVersion
}

func readClaudeVersion(path string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, path, "--version").CombinedOutput()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func (adapter *ClaudeAdapter) goos() string {
	if adapter.GOOS != "" {
		return adapter.GOOS
	}
	return runtime.GOOS
}

func claudeVersionStatus(known bool) string {
	if known {
		return "known"
	}
	return "unknown"
}

func (adapter *ClaudeAdapter) claudeInstallMessage(
	base string,
	selected claudeHookCommand,
) string {
	return base + claudeCompatibilityNote(selected)
}

func claudeCompatibilityNote(selected claudeHookCommand) string {
	if selected.Form != claudeHookFormShell {
		return ""
	}
	if selected.VersionKnown {
		return "; compatibility shell command selected for Claude Code " +
			selected.ClaudeVersion + "; supported Hook events: " +
			strings.Join(selected.Events, ", ")
	}
	return "; Claude Code version is unknown, so a conservative compatibility shell command and baseline Stop/Notification events are in use"
}

func claudeReceiptCompatibilityNote(receipt claudeReceipt) string {
	if receipt.HookForm != claudeHookFormShell {
		return ""
	}
	if receipt.VersionStatus == "known" && receipt.ClaudeVersion != "" {
		return "; compatibility shell command selected for Claude Code " +
			receipt.ClaudeVersion + "; supported Hook events: " +
			strings.Join(receipt.Events, ", ")
	}
	return "; Claude Code version is unknown, so a conservative compatibility shell command and baseline Stop/Notification events are in use"
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
	if receipt.Adapter != claudeAdapterID ||
		validateReceiptBridge(
			receipt.Version,
			receipt.BridgeProtocol,
			receipt.ActivationGeneration,
		) != nil ||
		!normalizeClaudeReceipt(&receipt) {
		return claudeReceipt{}, errors.New("invalid Claude Code adapter receipt")
	}
	return receipt, nil
}

func normalizeClaudeReceipt(receipt *claudeReceipt) bool {
	if receipt == nil || receipt.Command == "" {
		return false
	}
	if receipt.HookForm == "" {
		// Receipts written before Hook form negotiation were always exec-form.
		if len(receipt.Args) == 0 {
			return false
		}
		receipt.HookForm = claudeHookFormExec
	}
	switch receipt.HookForm {
	case claudeHookFormExec:
		if len(receipt.Args) == 0 {
			return false
		}
	case claudeHookFormShell:
		if len(receipt.Args) != 0 ||
			receipt.InvocationCommand == "" ||
			len(receipt.InvocationArgs) == 0 {
			return false
		}
	default:
		return false
	}
	events, ok := normalizeClaudeReceiptEvents(receipt.Events)
	if !ok {
		return false
	}
	receipt.Events = events
	return true
}

func normalizeClaudeReceiptEvents(events []string) ([]string, bool) {
	if len(events) == 0 {
		// Receipts written before event negotiation always installed every
		// AgentBell Claude Hook known at that time.
		return append([]string(nil), claudeHookEvents...), true
	}
	seen := make(map[string]bool, len(events))
	for _, eventName := range events {
		if !slices.Contains(claudeHookEvents, eventName) ||
			seen[eventName] {
			return nil, false
		}
		seen[eventName] = true
	}
	result := make([]string, 0, len(events))
	for _, eventName := range claudeHookEvents {
		if seen[eventName] {
			result = append(result, eventName)
		}
	}
	return result, true
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

func mergeClaudeHooks(
	root map[string]any,
	events []string,
	command string,
	args []string,
) (bool, error) {
	hooks, err := objectField(root, "hooks", true)
	if err != nil {
		return false, err
	}
	changed := false
	for _, eventName := range events {
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

func removeClaudeHooks(
	root map[string]any,
	events []string,
	command string,
	args []string,
) (bool, error) {
	hooks, err := objectField(root, "hooks", false)
	if err != nil {
		return false, err
	}
	if hooks == nil {
		return false, nil
	}
	changed := false
	for _, eventName := range events {
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
	result := map[string]any{
		"type":    "command",
		"command": command,
		"timeout": float64(5),
	}
	if len(args) == 0 {
		return result
	}
	values := make([]any, 0, len(args))
	for _, value := range args {
		values = append(values, value)
	}
	result["args"] = values
	return result
}

func matchesClaudeHandler(handler map[string]any, command string, args []string) bool {
	if handler["type"] != "command" || handler["command"] != command {
		return false
	}
	if len(args) == 0 {
		rawArgs, exists := handler["args"]
		if !exists {
			return true
		}
		values, ok := rawArgs.([]any)
		return ok && len(values) == 0
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
