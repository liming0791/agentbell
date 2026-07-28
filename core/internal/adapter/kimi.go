package adapter

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const kimiAdapterID = "kimi-code"

// kimiHookEvents mirrors the M0 plugin plugins/kimi/agentbell/kimi.plugin.json.
var kimiHookEvents = []string{"Stop", "StopFailure", "PermissionRequest"}

const (
	kimiRegionBeginPrefix = "# agentbell:kimi-code:begin sha256:"
	kimiRegionEndMarker   = "# agentbell:kimi-code:end"
)

var (
	tomlHeaderPattern      = regexp.MustCompile(`^\s*\[`)
	tomlInlineHooksPattern = regexp.MustCompile(`^hooks\s*=`)
)

type KimiAdapter struct {
	Executable       string
	BridgeExecutable string
	ActiveGeneration uint64
	StateDir         string
	KimiHome         string
	Now              func() time.Time
	LookPath         func(string) (string, error)
}

type kimiReceipt struct {
	Version              int       `json:"version"`
	Adapter              string    `json:"adapter"`
	HookPath             string    `json:"hookPath"`
	Command              string    `json:"command"`
	Backup               string    `json:"backup,omitempty"`
	InstalledAt          time.Time `json:"installedAt"`
	BridgeProtocol       int       `json:"bridgeProtocol,omitempty"`
	ActivationGeneration uint64    `json:"activationGeneration,omitempty"`
}

func NewKimiAdapter(executable, stateDir string) (*KimiAdapter, error) {
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
	home := os.Getenv("KIMI_CODE_HOME")
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		home = filepath.Join(userHome, ".kimi-code")
	}
	return &KimiAdapter{
		Executable: absolute,
		StateDir:   stateDir,
		KimiHome:   home,
		Now:        time.Now,
		LookPath:   exec.LookPath,
	}, nil
}

func (adapter *KimiAdapter) Detect() bool {
	if _, err := adapter.lookPath()("kimi"); err == nil {
		return true
	}
	_, err := os.Stat(adapter.configPath())
	return err == nil
}

func (adapter *KimiAdapter) Plan() AdapterPlan {
	return AdapterPlan{
		Adapter:    kimiAdapterID,
		Detected:   adapter.Detect(),
		HookPath:   adapter.configPath(),
		Executable: plannedHookExecutable(adapter.Executable, adapter.BridgeExecutable),
		Changes: []string{
			"append AgentBell [[hooks]] entries for Stop, StopFailure and PermissionRequest",
			"write an ownership receipt for precise uninstall",
		},
	}
}

func (adapter *KimiAdapter) Install(dryRun bool) (AdapterResult, error) {
	result := AdapterResult{
		Adapter: kimiAdapterID, Detected: adapter.Detect(), HookPath: adapter.configPath(),
	}
	command, err := adapter.command()
	if err != nil {
		return result, err
	}
	raw, exists, err := readOptionalFile(adapter.configPath())
	if err != nil {
		return result, err
	}
	updated, changed, err := installKimiRegion(string(raw), command)
	if err != nil {
		return result, err
	}
	result.Installed = true
	result.Changed = changed
	if dryRun || !changed {
		if !changed {
			result.Message = "AgentBell Kimi Code hooks are already installed"
		} else {
			result.Message = "AgentBell Kimi Code hooks would be installed; a new Kimi session is required to load them"
		}
		return result, nil
	}

	if err := os.MkdirAll(filepath.Dir(adapter.configPath()), 0o700); err != nil {
		return result, err
	}
	backup := ""
	if exists {
		backup, err = adapter.backup(adapter.configPath())
		if err != nil {
			return result, err
		}
	}
	if err := writeFileAtomic(adapter.configPath(), []byte(updated)); err != nil {
		return result, err
	}
	invocation, err := adapter.hookInvocation()
	if err != nil {
		return result, err
	}
	receipt := kimiReceipt{
		Version:              receiptVersion(invocation),
		Adapter:              kimiAdapterID,
		HookPath:             adapter.configPath(),
		Command:              command,
		Backup:               backup,
		InstalledAt:          adapter.now().UTC(),
		BridgeProtocol:       invocation.BridgeProtocol,
		ActivationGeneration: invocation.ActivationGeneration,
	}
	if err := adapter.writeReceipt(receipt); err != nil {
		return result, err
	}
	result.Backup = backup
	result.Message = "AgentBell Kimi Code hooks are installed; close the old session and start a new Kimi session"
	return result, nil
}

func (adapter *KimiAdapter) Verify() (AdapterResult, error) {
	result := AdapterResult{
		Adapter: kimiAdapterID, Detected: adapter.Detect(), HookPath: adapter.configPath(),
	}
	// 可执行文件移动后当前命令与安装时不同：回退到 receipt 里的命令校验，
	// 让 Verify 仍然能确认已安装的区域完好。
	receipt, receiptErr := adapter.readReceipt()
	command, commandErr := adapter.command()
	if receiptErr == nil {
		command = receipt.Command
	}
	if commandErr != nil && receiptErr != nil {
		return result, errors.Join(commandErr, receiptErr)
	}
	raw, err := os.ReadFile(adapter.configPath())
	if errors.Is(err, os.ErrNotExist) {
		return result, errors.New("AgentBell Kimi Code hooks are not installed")
	}
	if err != nil {
		return result, err
	}
	region, found, err := findKimiRegion(string(raw))
	if err != nil {
		return result, err
	}
	if !found {
		return result, errors.New("AgentBell Kimi Code hooks are not installed")
	}
	if region.hash != hashBytes([]byte(command)) {
		return result, errors.New("AgentBell Kimi Code hook command does not match the current Core")
	}
	body := string(raw)[region.start:region.end]
	for _, eventName := range kimiHookEvents {
		if !strings.Contains(body, "event = "+tomlBasicString(eventName)) {
			return result, fmt.Errorf("AgentBell Kimi Code hook for %s is missing", eventName)
		}
	}
	result.Installed = true
	result.Message = "AgentBell Kimi Code hooks are installed"
	return result, nil
}

func (adapter *KimiAdapter) Uninstall(dryRun bool) (AdapterResult, error) {
	result := AdapterResult{
		Adapter: kimiAdapterID, Detected: adapter.Detect(), HookPath: adapter.configPath(),
	}
	raw, exists, err := readOptionalFile(adapter.configPath())
	if err != nil {
		return result, err
	}
	if !exists {
		result.Message = "Kimi Code config file does not exist"
		return result, nil
	}
	region, found, err := findKimiRegion(string(raw))
	if err != nil {
		return result, err
	}
	if !found {
		result.Message = "AgentBell Kimi Code hooks are not installed"
		return result, nil
	}
	content := string(raw)
	updated := content[:region.start] + content[region.end:]
	result.Changed = true
	if dryRun {
		result.Message = "AgentBell Kimi Code hooks would be uninstalled"
		return result, nil
	}

	backup, err := adapter.backup(adapter.configPath())
	if err != nil {
		return result, err
	}
	if err := writeFileAtomic(adapter.configPath(), []byte(updated)); err != nil {
		return result, err
	}
	_ = os.Remove(adapter.receiptPath())
	result.Backup = backup
	result.Message = "AgentBell Kimi Code hooks are uninstalled"
	return result, nil
}

func (adapter *KimiAdapter) Diagnose() AdapterResult {
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
			kimiAdapterID,
			adapter.configPath(),
			adapter.ActiveGeneration,
		)
	} else {
		proof, verified = runtimeProofAfterConfig(
			adapter.StateDir,
			kimiAdapterID,
			adapter.configPath(),
		)
	}
	result.RuntimeVerified = verified
	if !proof.LastSeen.IsZero() {
		result.LastSeen = proof.LastSeen.Format(time.RFC3339Nano)
	}
	if verified {
		result.Message = "AgentBell Kimi Code hooks are installed and have run since the last config change"
	} else {
		result.Message = "Kimi Code hooks are installed but not yet observed after the last config change; start a new Kimi session"
	}
	return result
}

func (adapter *KimiAdapter) configPath() string {
	return filepath.Join(adapter.KimiHome, "config.toml")
}

func (adapter *KimiAdapter) command() (string, error) {
	invocation, err := adapter.hookInvocation()
	if err != nil {
		return "", err
	}
	return invocation.shellCommand(false), nil
}

func (adapter *KimiAdapter) hookInvocation() (hookInvocation, error) {
	return resolveHookInvocation(
		adapter.Executable,
		adapter.BridgeExecutable,
		adapter.ActiveGeneration,
		kimiAdapterID,
		"cli",
		"host",
	)
}

func (adapter *KimiAdapter) backup(source string) (string, error) {
	value, err := os.ReadFile(source)
	if err != nil {
		return "", err
	}
	directory := filepath.Join(adapter.StateDir, "adapters", kimiAdapterID, "backups")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	name := fmt.Sprintf(
		"config-%s-%s.toml",
		adapter.now().UTC().Format("20060102T150405.000000000Z"),
		hashBytes(value)[:12],
	)
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, value, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func (adapter *KimiAdapter) receiptPath() string {
	return filepath.Join(adapter.StateDir, "adapters", kimiAdapterID, "receipt.json")
}

func (adapter *KimiAdapter) writeReceipt(receipt kimiReceipt) error {
	return writeJSONFile(adapter.receiptPath(), receipt)
}

func (adapter *KimiAdapter) readReceipt() (kimiReceipt, error) {
	value, err := os.ReadFile(adapter.receiptPath())
	if err != nil {
		return kimiReceipt{}, err
	}
	var receipt kimiReceipt
	if err := json.Unmarshal(value, &receipt); err != nil {
		return kimiReceipt{}, err
	}
	if receipt.Adapter != kimiAdapterID ||
		validateReceiptBridge(
			receipt.Version,
			receipt.BridgeProtocol,
			receipt.ActivationGeneration,
		) != nil {
		return kimiReceipt{}, errors.New("invalid Kimi Code adapter receipt")
	}
	return receipt, nil
}

func (adapter *KimiAdapter) now() time.Time {
	if adapter.Now != nil {
		return adapter.Now()
	}
	return time.Now()
}

func (adapter *KimiAdapter) lookPath() func(string) (string, error) {
	if adapter.LookPath != nil {
		return adapter.LookPath
	}
	return exec.LookPath
}

// kimiRegion 是 config.toml 里 AgentBell 标记区域的字节范围，
// end 为 end 标记行之后（含换行）的偏移。
type kimiRegion struct {
	start int
	end   int
	hash  string
}

func findKimiRegion(content string) (kimiRegion, bool, error) {
	var region kimiRegion
	begins := 0
	ends := 0
	offset := 0
	for offset < len(content) {
		newline := strings.IndexByte(content[offset:], '\n')
		lineEnd := len(content)
		if newline >= 0 {
			lineEnd = offset + newline
		}
		line := strings.TrimSpace(content[offset:lineEnd])
		next := lineEnd + 1
		if strings.HasPrefix(line, kimiRegionBeginPrefix) {
			begins++
			region.start = offset
			region.hash = strings.TrimPrefix(line, kimiRegionBeginPrefix)
		}
		if line == kimiRegionEndMarker {
			ends++
			region.end = next
			if region.end > len(content) {
				region.end = len(content)
			}
		}
		offset = next
	}
	if begins == 0 && ends == 0 {
		return kimiRegion{}, false, nil
	}
	if begins != 1 || ends != 1 || region.start >= region.end {
		return kimiRegion{}, false, errors.New(
			"AgentBell markers in config.toml are malformed; please fix them manually",
		)
	}
	return region, true, nil
}

// hasInlineHooksConflict 报告顶层（任何表头之前）的 `hooks = ...` 内联写法；
// 在其后追加 [[hooks]] 数组表会产生非法 TOML，必须拒绝安装。
func hasInlineHooksConflict(content string) bool {
	inRoot := true
	offset := 0
	for offset < len(content) {
		newline := strings.IndexByte(content[offset:], '\n')
		lineEnd := len(content)
		if newline >= 0 {
			lineEnd = offset + newline
		}
		line := strings.TrimSpace(content[offset:lineEnd])
		offset = lineEnd + 1
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if tomlHeaderPattern.MatchString(line) {
			inRoot = false
			continue
		}
		if inRoot && tomlInlineHooksPattern.MatchString(line) {
			return true
		}
	}
	return false
}

func kimiRegionText(command string) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "%s%s\n", kimiRegionBeginPrefix, hashBytes([]byte(command)))
	for _, eventName := range kimiHookEvents {
		builder.WriteString("[[hooks]]\n")
		builder.WriteString("event = " + tomlBasicString(eventName) + "\n")
		builder.WriteString("command = " + tomlBasicString(command) + "\n")
		builder.WriteString("timeout = 5\n\n")
	}
	builder.WriteString(kimiRegionEndMarker + "\n")
	return builder.String()
}

// installKimiRegion 幂等地安装/升级标记区域：哈希一致则不改动，
// 哈希不同（Core 路径变化）则原地替换，没有区域则追加到文件末尾。
func installKimiRegion(content, command string) (string, bool, error) {
	region, found, err := findKimiRegion(content)
	if err != nil {
		return "", false, err
	}
	text := kimiRegionText(command)
	if found {
		if region.hash == hashBytes([]byte(command)) {
			return content, false, nil
		}
		return content[:region.start] + text + content[region.end:], true, nil
	}
	if hasInlineHooksConflict(content) {
		return "", false, errors.New(
			"config.toml declares a top-level `hooks = ...` inline value; " +
				"appending [[hooks]] tables would be invalid TOML, migrate the inline hooks manually",
		)
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return content + text, true, nil
}

// tomlBasicString 用 TOML 基本字符串字面量包裹 value：
// 转义反斜杠与双引号（可执行路径中的引号已在 command() 拒绝）。
func tomlBasicString(value string) string {
	escaped := strings.ReplaceAll(value, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return `"` + escaped + `"`
}

func readOptionalFile(path string) ([]byte, bool, error) {
	value, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return value, true, nil
}
