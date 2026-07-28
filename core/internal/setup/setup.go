// Package setup implements the interactive `agentbell setup` flow: it detects
// coding agents, installs and configures lark-cli, selects a Feishu
// notification chat, writes the AgentBell config, and installs agent hooks.
package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/liming0791/agentbell/core/internal/adapter"
	"github.com/liming0791/agentbell/core/internal/config"
	"github.com/liming0791/agentbell/core/internal/event"
)

const larkInstallCommand = "npx @larksuite/cli@latest install"

const defaultChatName = "AgentBell 通知"

const captureTimeout = 30 * time.Second

// hookAdapter is the slice of an agent adapter the setup flow needs.
type hookAdapter interface {
	Install(dryRun bool) (adapter.AdapterResult, error)
	Verify() (adapter.AdapterResult, error)
}

// Setup carries the dependencies of the setup flow. Zero-value fields fall
// back to real implementations, so tests can inject fakes selectively.
type Setup struct {
	Runner   Runner
	Prompter Prompter
	LookPath func(string) (string, error)
	Getenv   func(string) string
	Now      func() time.Time
	Stat     func(string) (os.FileInfo, error)

	HomeDir    string
	ConfigFile string
	StateDir   string
	// CoreExecutable is the selected active Core used by direct-hook adapters.
	// BridgeExecutable and ActiveGeneration select the version-independent
	// bridge for adapters whose host products support lifecycle hooks.
	CoreExecutable   string
	BridgeExecutable string
	ActiveGeneration uint64
	GOOS             string
	GOARCH           string
	DryRun           bool
	Out              io.Writer

	NewCodexAdapter     func() (hookAdapter, error)
	NewClaudeAdapter    func() (hookAdapter, error)
	NewKimiAdapter      func() (hookAdapter, error)
	NewOpenCodeAdapter  func() (hookAdapter, error)
	NewQoderAdapter     func() (hookAdapter, error)
	NewQoderWorkAdapter func() (hookAdapter, error)
	NewTraeAdapter      func() (hookAdapter, error)
	InstallService      func(context.Context) (string, error)
	CreateBinding       func(context.Context, BindingRequest) (BindingResult, error)
}

// AgentStatus describes one detected coding agent.
type AgentStatus struct {
	ID       string `json:"id"`
	Detected bool   `json:"detected"`
	Source   string `json:"source,omitempty"`
	Adapter  string `json:"adapter"`
}

// LarkCLIStatus describes the lark-cli installation.
type LarkCLIStatus struct {
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	Path      string `json:"path,omitempty"`
}

type BindingRequest struct {
	ChannelName string
	Identity    string
	LarkCLIPath string
	TTL         time.Duration
}

type BindingResult struct {
	Code        string    `json:"code"`
	ChannelName string    `json:"channelName"`
	Identity    string    `json:"identity"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

type destinationSelection struct {
	Channel *config.Channel
	Binding *BindingResult
}

// Report is the structured result of a setup run.
type Report struct {
	DryRun        bool            `json:"dryRun"`
	Platform      string          `json:"platform"`
	Architecture  string          `json:"architecture"`
	Agents        []AgentStatus   `json:"agents"`
	LarkCLI       LarkCLIStatus   `json:"larkCli"`
	Channel       *config.Channel `json:"channel,omitempty"`
	ConfigFile    string          `json:"configFile,omitempty"`
	Backup        string          `json:"backup,omitempty"`
	CodexHook     string          `json:"codexHook,omitempty"`
	ClaudeHook    string          `json:"claudeHook,omitempty"`
	KimiHook      string          `json:"kimiHook,omitempty"`
	OpenCodeHook  string          `json:"openCodeHook,omitempty"`
	QoderHook     string          `json:"qoderHook,omitempty"`
	QoderWorkHook string          `json:"qoderWorkHook,omitempty"`
	TraeHook      string          `json:"traeHook,omitempty"`
	Service       string          `json:"service,omitempty"`
	Binding       *BindingResult  `json:"binding,omitempty"`
}

type knownAgent struct {
	id         string
	binary     string
	configEnv  string
	configDirs []string
	platforms  []string
}

var knownAgents = []knownAgent{
	{id: "codex", binary: "codex", configEnv: "CODEX_HOME", configDirs: []string{".codex"}},
	{id: "claude", binary: "claude", configEnv: "CLAUDE_CONFIG_DIR", configDirs: []string{".claude"}},
	{id: "kimi", binary: "kimi", configEnv: "KIMI_CODE_HOME", configDirs: []string{".kimi-code", ".kimi"}},
	{id: "opencode", binary: "opencode", configDirs: []string{".opencode", filepath.Join(".config", "opencode")}},
	{id: "qoder", binary: "qoder", configDirs: []string{".qoder"}},
	{
		id: "qoder-work", binary: "qoderwork",
		configDirs: []string{
			".qoderwork",
			filepath.Join("Applications", "QoderWork.app"),
			filepath.Join("AppData", "Local", "Programs", "QoderWork", "QoderWork.exe"),
			filepath.Join("AppData", "Local", "QoderWork", "QoderWork.exe"),
		},
		platforms: []string{"darwin", "windows"},
	},
	{
		id: "trae", binary: "trae",
		configDirs: []string{
			".trae",
			filepath.Join("Applications", "TRAE.app"),
			filepath.Join("AppData", "Local", "Programs", "TRAE", "TRAE.exe"),
			filepath.Join("AppData", "Local", "TRAE", "TRAE.exe"),
		},
		platforms: []string{"darwin", "windows"},
	},
}

func (setup *Setup) resolve() error {
	if setup.Runner == nil {
		setup.Runner = ExecRunner{}
	}
	if setup.LookPath == nil {
		setup.LookPath = exec.LookPath
	}
	if setup.Getenv == nil {
		setup.Getenv = os.Getenv
	}
	if setup.Now == nil {
		setup.Now = time.Now
	}
	if setup.Stat == nil {
		setup.Stat = os.Stat
	}
	if setup.Out == nil {
		setup.Out = os.Stdout
	}
	if setup.GOOS == "" {
		setup.GOOS = runtime.GOOS
	}
	if setup.GOARCH == "" {
		setup.GOARCH = runtime.GOARCH
	}
	if setup.Prompter == nil {
		setup.Prompter = NewStdioPrompter(os.Stdin, setup.Out)
	}
	if setup.HomeDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		setup.HomeDir = home
	}
	if setup.NewCodexAdapter == nil {
		setup.NewCodexAdapter = func() (hookAdapter, error) {
			value, err := adapter.NewCodexAdapter(
				setup.CoreExecutable,
				setup.StateDir,
			)
			if err != nil {
				return nil, err
			}
			value.BridgeExecutable = setup.BridgeExecutable
			value.ActiveGeneration = setup.ActiveGeneration
			return value, nil
		}
	}
	if setup.NewClaudeAdapter == nil {
		setup.NewClaudeAdapter = func() (hookAdapter, error) {
			value, err := adapter.NewClaudeAdapter(
				setup.CoreExecutable,
				setup.StateDir,
			)
			if err != nil {
				return nil, err
			}
			value.BridgeExecutable = setup.BridgeExecutable
			value.ActiveGeneration = setup.ActiveGeneration
			return value, nil
		}
	}
	if setup.NewKimiAdapter == nil {
		setup.NewKimiAdapter = func() (hookAdapter, error) {
			value, err := adapter.NewKimiAdapter(
				setup.CoreExecutable,
				setup.StateDir,
			)
			if err != nil {
				return nil, err
			}
			value.BridgeExecutable = setup.BridgeExecutable
			value.ActiveGeneration = setup.ActiveGeneration
			return value, nil
		}
	}
	if setup.NewOpenCodeAdapter == nil {
		setup.NewOpenCodeAdapter = func() (hookAdapter, error) {
			return adapter.NewOpenCodeAdapter(setup.CoreExecutable, setup.StateDir)
		}
	}
	if setup.NewQoderAdapter == nil {
		setup.NewQoderAdapter = func() (hookAdapter, error) {
			return adapter.NewQoderAdapter(setup.CoreExecutable, setup.StateDir)
		}
	}
	if setup.NewQoderWorkAdapter == nil {
		setup.NewQoderWorkAdapter = func() (hookAdapter, error) {
			return adapter.NewQoderWorkAdapter(setup.CoreExecutable, setup.StateDir)
		}
	}
	if setup.NewTraeAdapter == nil {
		setup.NewTraeAdapter = func() (hookAdapter, error) {
			return adapter.NewTraeAdapter(setup.CoreExecutable, setup.StateDir)
		}
	}
	return nil
}

func (setup *Setup) printf(format string, args ...any) {
	fmt.Fprintf(setup.Out, format+"\n", args...)
}

func (setup *Setup) capture(ctx context.Context, args ...string) ([]byte, error) {
	captureCtx, cancel := context.WithTimeout(ctx, captureTimeout)
	defer cancel()
	return setup.Runner.Capture(captureCtx, "lark-cli", args...)
}

// Run executes the setup flow and returns a structured report.
func (setup *Setup) Run(ctx context.Context) (*Report, error) {
	if err := setup.resolve(); err != nil {
		return nil, err
	}
	report := &Report{
		DryRun:       setup.DryRun,
		Platform:     setup.GOOS,
		Architecture: setup.GOARCH,
		ConfigFile:   setup.ConfigFile,
	}
	report.Agents = setup.detectAgents()
	report.LarkCLI = setup.detectLarkCLI(ctx, !setup.DryRun)

	if setup.DryRun {
		setup.printPlan(report)
		return report, nil
	}

	if err := setup.ensureLarkCLI(ctx, report); err != nil {
		return report, err
	}
	if err := setup.ensureLarkConfig(ctx); err != nil {
		return report, err
	}
	if err := setup.ensureLarkAuth(ctx); err != nil {
		return report, err
	}
	selection, err := setup.selectDestination(ctx, report.LarkCLI.Path)
	if err != nil {
		return report, err
	}
	if selection.Binding != nil {
		report.Binding = selection.Binding
		setup.printf("")
		setup.printf("一次性绑定已创建。请将以下代码作为目标飞书会话中的完整消息发送：")
		setup.printf("%s", selection.Binding.Code)
		setup.printf("然后运行：agentbell bind complete --code-stdin")
		setup.printf("绑定完成后再次运行 `agentbell setup` 安装 Hook 和后台服务。")
		return report, nil
	}
	channel := *selection.Channel
	report.Channel = &channel
	backup, err := setup.writeConfig(channel)
	if err != nil {
		return report, err
	}
	report.Backup = backup
	if err := setup.installAdapters(ctx, report); err != nil {
		return report, err
	}
	if managedServicePlatform(setup.GOOS) && setup.InstallService != nil {
		confirmed, err := setup.Prompter.Confirm(
			fmt.Sprintf("是否安装 %s 后台通知服务？", serviceDisplayName(setup.GOOS)),
		)
		if err != nil {
			return report, err
		}
		if confirmed {
			servicePath, err := setup.InstallService(ctx)
			if err != nil {
				return report, fmt.Errorf("安装后台通知服务失败：%w", err)
			}
			report.Service = servicePath
			setup.printf("后台通知服务已安装并启动（%s）", servicePath)
		} else {
			setup.printf("已跳过后台服务安装，可稍后运行 `agentbell service install`")
		}
	}
	setup.printf("")
	setup.printf("设置完成！下一步：")
	if managedServicePlatform(setup.GOOS) {
		if report.Service == "" {
			setup.printf("  1. 安装并启动后台通知服务：agentbell service install")
		} else {
			setup.printf("  1. 后台通知服务已启动；可用 `agentbell service status` 检查")
		}
	} else {
		setup.printf("  1. 启动通知服务：agentbell service run --foreground")
	}
	setup.printf("  2. 发送测试消息：agentbell test")
	return report, nil
}

func (setup *Setup) detectAgents() []AgentStatus {
	result := make([]AgentStatus, 0, len(knownAgents))
	for _, agent := range knownAgents {
		status := AgentStatus{ID: agent.id, Adapter: "pending"}
		switch agent.id {
		case "codex":
			status.Adapter = "codex"
		case "claude":
			status.Adapter = "claude-code"
		case "kimi":
			status.Adapter = "kimi-code"
		case "opencode":
			status.Adapter = "opencode"
		case "qoder":
			status.Adapter = "qoder"
		case "qoder-work":
			status.Adapter = "qoder-work"
		case "trae":
			status.Adapter = "trae"
		}
		if len(agent.platforms) > 0 && !containsString(agent.platforms, setup.GOOS) {
			result = append(result, status)
			continue
		}
		if _, err := setup.LookPath(agent.binary); err == nil {
			status.Detected = true
			status.Source = "path"
		} else {
			if agent.configEnv != "" {
				if directory := setup.Getenv(agent.configEnv); directory != "" {
					if _, err := setup.Stat(directory); err == nil {
						status.Detected = true
						status.Source = "config-env"
					}
				}
			}
			for _, directory := range agent.configDirs {
				if status.Detected {
					break
				}
				if _, err := setup.Stat(filepath.Join(setup.HomeDir, directory)); err == nil {
					status.Detected = true
					status.Source = "config-dir"
					break
				}
			}
			for _, path := range desktopInstallPaths(setup.GOOS, setup.Getenv, agent.id) {
				if status.Detected {
					break
				}
				if path != "" {
					if _, err := setup.Stat(path); err == nil {
						status.Detected = true
						status.Source = "application"
					}
				}
			}
		}
		result = append(result, status)
	}
	return result
}

func desktopInstallPaths(
	goos string,
	getenv func(string) string,
	agentID string,
) []string {
	appNames := []string{}
	executableName := ""
	switch agentID {
	case "qoder-work":
		appNames = []string{"QoderWork.app", "QoderWork CN.app"}
		executableName = "QoderWork.exe"
	case "trae":
		appNames = []string{"TRAE.app", "Trae CN.app"}
		executableName = "TRAE.exe"
	default:
		return nil
	}
	switch goos {
	case "darwin":
		paths := make([]string, 0, len(appNames))
		for _, appName := range appNames {
			paths = append(
				paths,
				filepath.Join(string(filepath.Separator), "Applications", appName),
			)
		}
		return paths
	case "windows":
		productName := strings.TrimSuffix(executableName, ".exe")
		paths := []string{}
		if localAppData := getenv("LOCALAPPDATA"); localAppData != "" {
			paths = append(
				paths,
				filepath.Join(localAppData, "Programs", productName, executableName),
				filepath.Join(localAppData, productName, executableName),
			)
		}
		if programFiles := getenv("ProgramFiles"); programFiles != "" {
			paths = append(paths, filepath.Join(programFiles, productName, executableName))
		}
		return paths
	default:
		return nil
	}
}

func (setup *Setup) detectLarkCLI(ctx context.Context, withVersion bool) LarkCLIStatus {
	status := LarkCLIStatus{}
	resolved, err := setup.LookPath("lark-cli")
	if err != nil {
		return status
	}
	status.Installed = true
	if absolute, err := filepath.Abs(resolved); err == nil {
		status.Path = absolute
	} else {
		status.Path = resolved
	}
	if withVersion {
		if output, err := setup.capture(ctx, "--version"); err == nil {
			status.Version = strings.TrimSpace(string(output))
		}
	}
	return status
}

func (setup *Setup) printPlan(report *Report) {
	setup.printf("[dry-run] 平台：%s/%s", report.Platform, report.Architecture)
	for _, agent := range report.Agents {
		if agent.Detected {
			setup.printf("[dry-run] 检测到代理：%s（%s）", agent.ID, agent.Source)
		}
	}
	if report.LarkCLI.Installed {
		setup.printf("[dry-run] lark-cli：已安装")
	} else {
		setup.printf("[dry-run] lark-cli：未安装")
	}
	setup.printf("[dry-run] 计划执行以下步骤（不会做任何修改）：")
	setup.printf("  1. 检查 lark-cli，缺失时通过 `%s` 安装", larkInstallCommand)
	setup.printf("  2. 检查 lark-cli 应用配置，缺失时运行 `lark-cli config init`")
	setup.printf("  3. 检查登录状态，未登录时运行 `lark-cli auth login --domain im`")
	setup.printf("  4. 搜索已有群聊或创建新的通知群")
	setup.printf("  5. 写入配置文件 %s（已存在时先备份再合并）", setup.ConfigFile)
	setup.printf("  6. 为检测到的 Codex / Claude Code / Kimi Code / OpenCode / Qoder / QoderWork / TRAE 安装 AgentBell 通知钩子")
	if managedServicePlatform(setup.GOOS) {
		setup.printf(
			"  7. 安装 %s，让通知服务登录后常驻",
			serviceDisplayName(setup.GOOS),
		)
	}
}

func managedServicePlatform(goos string) bool {
	return goos == "darwin" || goos == "linux" || goos == "windows"
}

func serviceDisplayName(goos string) string {
	switch goos {
	case "darwin":
		return "macOS LaunchAgent"
	case "windows":
		return "Windows 登录计划任务"
	case "linux":
		return "Linux 用户服务"
	default:
		return "后台通知服务"
	}
}

func (setup *Setup) ensureLarkCLI(ctx context.Context, report *Report) error {
	if report.LarkCLI.Installed {
		setup.printf("lark-cli 已安装（%s）", report.LarkCLI.Version)
		return nil
	}
	setup.printf("未检测到 lark-cli，安装命令：%s", larkInstallCommand)
	confirmed, err := setup.Prompter.Confirm("是否现在安装 lark-cli？")
	if err != nil {
		return err
	}
	if !confirmed {
		return errors.New("setup 需要 lark-cli，请先手动安装后重试")
	}
	if err := setup.Runner.Interactive(ctx, "npx", "@larksuite/cli@latest", "install"); err != nil {
		return fmt.Errorf("安装 lark-cli 失败：%w", err)
	}
	if _, err := setup.LookPath("lark-cli"); err != nil {
		return errors.New("lark-cli 安装后仍不可用，请检查 PATH 配置")
	}
	report.LarkCLI = setup.detectLarkCLI(ctx, true)
	return nil
}

func (setup *Setup) larkConfigured(ctx context.Context) bool {
	output, err := setup.capture(ctx, "config", "show")
	return err == nil && strings.Contains(string(output), "appId")
}

func (setup *Setup) ensureLarkConfig(ctx context.Context) error {
	if setup.larkConfigured(ctx) {
		setup.printf("lark-cli 应用配置已存在")
		return nil
	}
	setup.printf("未检测到 lark-cli 应用配置（需要飞书开放平台应用的 appId/appSecret）")
	confirmed, err := setup.Prompter.Confirm("是否现在运行 `lark-cli config init` 进行配置？")
	if err != nil {
		return err
	}
	if !confirmed {
		return errors.New("setup 需要 lark-cli 应用配置，请运行 `lark-cli config init` 后重试")
	}
	if err := setup.Runner.Interactive(ctx, "lark-cli", "config", "init"); err != nil {
		return fmt.Errorf("lark-cli config init 失败：%w", err)
	}
	if !setup.larkConfigured(ctx) {
		return errors.New("lark-cli 应用配置仍不可用，请检查 `lark-cli config show`")
	}
	return nil
}

func (setup *Setup) larkAuthorized(ctx context.Context) bool {
	_, err := setup.capture(ctx, "auth", "status", "--verify")
	return err == nil
}

func (setup *Setup) ensureLarkAuth(ctx context.Context) error {
	if setup.larkAuthorized(ctx) {
		setup.printf("飞书登录状态正常")
		return nil
	}
	setup.printf("lark-cli 尚未登录或授权已过期")
	confirmed, err := setup.Prompter.Confirm("是否现在运行 `lark-cli auth login --domain im` 登录？")
	if err != nil {
		return err
	}
	if !confirmed {
		return errors.New("setup 需要飞书登录，请运行 `lark-cli auth login --domain im` 后重试")
	}
	if err := setup.Runner.Interactive(ctx, "lark-cli", "auth", "login", "--domain", "im"); err != nil {
		return fmt.Errorf("lark-cli auth login 失败：%w", err)
	}
	if !setup.larkAuthorized(ctx) {
		return errors.New("登录验证仍未通过，请检查 `lark-cli auth status`")
	}
	return nil
}

type chatSummary struct {
	ID   string `json:"chat_id"`
	Name string `json:"name"`
}

type chatSearchResponse struct {
	Data struct {
		Chats []chatSummary `json:"chats"`
	} `json:"data"`
}

var chatIDPattern = regexp.MustCompile(`"chat_id"\s*:\s*"(oc_[^"]+)"`)

// authStatus 是 `lark-cli auth status` 输出中与 setup 相关的字段。
type authStatus struct {
	Identity   string `json:"identity"`
	UserOpenID string `json:"userOpenId"`
}

// currentAuth 读取当前登录身份。失败时返回零值，调用方按无用户身份降级。
func (setup *Setup) currentAuth(ctx context.Context) authStatus {
	output, err := setup.capture(ctx, "auth", "status")
	if err != nil {
		return authStatus{}
	}
	var status authStatus
	if err := json.Unmarshal(output, &status); err != nil {
		return authStatus{}
	}
	return status
}

func (setup *Setup) selectDestination(
	ctx context.Context,
	larkCLIPath string,
) (destinationSelection, error) {
	options := []string{"搜索已有群聊", "新建通知群"}
	if setup.CreateBinding != nil {
		options = append(options, "使用一次性绑定码")
	}
	choice, err := setup.Prompter.Select("请选择通知群聊", options)
	if err != nil {
		return destinationSelection{}, err
	}
	if choice == 1 {
		channel, err := setup.createChat(ctx)
		return destinationSelection{Channel: &channel}, err
	}
	if choice == 2 && setup.CreateBinding != nil {
		channelName, err := setup.Prompter.Input("请输入通知通道名称")
		if err != nil {
			return destinationSelection{}, err
		}
		channelName = strings.TrimSpace(channelName)
		if channelName == "" {
			return destinationSelection{}, errors.New("通知通道名称不能为空")
		}
		identityIndex, err := setup.Prompter.Select(
			"请选择发送身份",
			[]string{"bot", "user"},
		)
		if err != nil {
			return destinationSelection{}, err
		}
		result, err := setup.CreateBinding(ctx, BindingRequest{
			ChannelName: channelName,
			Identity:    []string{"bot", "user"}[identityIndex],
			LarkCLIPath: larkCLIPath,
			TTL:         10 * time.Minute,
		})
		if err != nil {
			return destinationSelection{}, err
		}
		return destinationSelection{Binding: &result}, nil
	}
	channel, err := setup.searchChat(ctx)
	return destinationSelection{Channel: &channel}, err
}

func (setup *Setup) searchChat(ctx context.Context) (config.Channel, error) {
	keyword, err := setup.Prompter.Input("请输入群聊搜索关键词")
	if err != nil {
		return config.Channel{}, err
	}
	if keyword == "" {
		return config.Channel{}, errors.New("搜索关键词不能为空")
	}
	output, err := setup.capture(ctx, "im", "+chat-search", "--query", keyword, "--format", "json")
	if err != nil {
		return config.Channel{}, fmt.Errorf("搜索群聊失败：%w", err)
	}
	var response chatSearchResponse
	if err := json.Unmarshal(output, &response); err != nil {
		return config.Channel{}, fmt.Errorf("解析群聊搜索结果失败：%w", err)
	}
	if len(response.Data.Chats) == 0 {
		return config.Channel{}, fmt.Errorf("没有找到与 %q 匹配的群聊", keyword)
	}
	options := make([]string, 0, len(response.Data.Chats))
	for _, chat := range response.Data.Chats {
		options = append(options, fmt.Sprintf("%s（%s）", chat.Name, chat.ID))
	}
	selected, err := setup.Prompter.Select("选择要接收通知的群聊", options)
	if err != nil {
		return config.Channel{}, err
	}
	chat := response.Data.Chats[selected]
	setup.printf("已选择群聊：%s（%s）", chat.Name, chat.ID)
	return channelForChat(chat.ID, chat.Name), nil
}

func (setup *Setup) createChat(ctx context.Context) (config.Channel, error) {
	name, err := setup.Prompter.Input(fmt.Sprintf("新群聊名称（直接回车使用默认：%s）", defaultChatName))
	if err != nil {
		return config.Channel{}, err
	}
	if name == "" {
		name = defaultChatName
	}
	args := []string{
		"im", "+chat-create",
		"--name", name,
		"--chat-mode", "group",
		"--type", "private",
	}
	// bot 身份建群时用户不在群内，需要显式邀请，否则用户收不到通知。
	if status := setup.currentAuth(ctx); status.Identity == "bot" && status.UserOpenID != "" {
		args = append(args, "--users", status.UserOpenID)
	}
	args = append(args, "--format", "json")
	output, err := setup.capture(ctx, args...)
	if err != nil {
		return config.Channel{}, fmt.Errorf("创建群聊失败：%w", err)
	}
	match := chatIDPattern.FindSubmatch(output)
	if match == nil {
		return config.Channel{}, fmt.Errorf("无法从 lark-cli 输出中解析 chat_id：%s", output)
	}
	chatID := string(match[1])
	setup.printf("已创建群聊：%s（%s）", name, chatID)
	return channelForChat(chatID, name), nil
}

func channelForChat(chatID, name string) config.Channel {
	if name == "" {
		name = "飞书通知群"
	}
	return config.Channel{
		ID:     "feishu",
		Name:   name,
		Type:   "feishu",
		ChatID: chatID,
		As:     "bot",
	}
}

func defaultNotifications() config.Notifications {
	return config.Notifications{
		Events:       []string{"task.completed", "task.failed", "agent.waiting", "approval.required"},
		PrivacyLevel: event.PrivacyMetadataOnly,
	}
}

// writeConfig saves the channel into the AgentBell config. An existing config
// is backed up next to the original file and merged: existing channels are
// preserved and the channel with the same id is updated in place, which keeps
// repeated setup runs idempotent.
func (setup *Setup) writeConfig(channel config.Channel) (string, error) {
	larkPath := ""
	if resolved, err := setup.LookPath("lark-cli"); err == nil {
		if absolute, absoluteErr := filepath.Abs(resolved); absoluteErr == nil {
			larkPath = absolute
		} else {
			larkPath = resolved
		}
	}
	value := config.Config{
		DefaultChannel: channel.ID,
		LarkCLIPath:    larkPath,
		Notifications:  defaultNotifications(),
		Channels:       []config.Channel{channel},
	}
	backup := ""
	existing, err := config.Load(setup.ConfigFile)
	switch {
	case err == nil:
		setup.printf("检测到已有配置，将备份后合并")
		raw, readErr := os.ReadFile(setup.ConfigFile)
		if readErr != nil {
			return "", readErr
		}
		backup = fmt.Sprintf("%s.bak-%s", setup.ConfigFile, setup.Now().UTC().Format("20060102T150405"))
		if err := os.WriteFile(backup, raw, 0o600); err != nil {
			return "", err
		}
		value = existing
		if larkPath != "" {
			value.LarkCLIPath = larkPath
		}
		merged := false
		for index, candidate := range value.Channels {
			if candidate.ID == channel.ID {
				value.Channels[index] = channel
				merged = true
				break
			}
		}
		if !merged {
			value.Channels = append(value.Channels, channel)
		}
		if value.DefaultChannel == "" {
			value.DefaultChannel = channel.ID
		}
		if len(value.Notifications.Events) == 0 && value.Notifications.PrivacyLevel == "" {
			value.Notifications = defaultNotifications()
		}
	case errors.Is(err, config.ErrNotFound):
		// fresh install, value already holds the defaults
	default:
		return "", fmt.Errorf("已有配置无法解析，请修复或删除 %s 后重试：%w", setup.ConfigFile, err)
	}
	if err := config.Save(setup.ConfigFile, &value); err != nil {
		return "", err
	}
	if backup != "" {
		setup.printf("原配置已备份到 %s", backup)
	}
	setup.printf("配置已写入 %s（默认频道：%s）", setup.ConfigFile, value.DefaultChannel)
	return backup, nil
}

func (setup *Setup) installAdapters(ctx context.Context, report *Report) error {
	for _, agent := range report.Agents {
		if !agent.Detected {
			continue
		}
		var displayName, adapterID string
		var newAdapter func() (hookAdapter, error)
		switch agent.ID {
		case "codex":
			displayName, adapterID, newAdapter = "Codex", "codex", setup.NewCodexAdapter
		case "claude":
			displayName, adapterID, newAdapter = "Claude Code", "claude-code", setup.NewClaudeAdapter
		case "kimi":
			displayName, adapterID, newAdapter = "Kimi Code", "kimi-code", setup.NewKimiAdapter
		case "opencode":
			displayName, adapterID, newAdapter = "OpenCode", "opencode", setup.NewOpenCodeAdapter
		case "qoder":
			displayName, adapterID, newAdapter = "Qoder", "qoder", setup.NewQoderAdapter
		case "qoder-work":
			displayName, adapterID, newAdapter = "QoderWork", "qoder-work", setup.NewQoderWorkAdapter
		case "trae":
			displayName, adapterID, newAdapter = "TRAE", "trae", setup.NewTraeAdapter
		default:
			setup.printf("%s：适配器尚未实现（后续切片），跳过", agent.ID)
			continue
		}
		confirmed, err := setup.Prompter.Confirm(
			fmt.Sprintf("检测到 %s，是否安装 AgentBell 通知钩子？", displayName),
		)
		if err != nil {
			return err
		}
		if !confirmed {
			setup.printf(
				"已跳过 %s 钩子安装，可稍后运行 `agentbell adapter install %s`",
				displayName,
				adapterID,
			)
			continue
		}
		hooks, err := newAdapter()
		if err != nil {
			return err
		}
		result, err := hooks.Install(false)
		if err != nil {
			return fmt.Errorf("安装 %s 钩子失败：%w", displayName, err)
		}
		if agent.ID == "codex" {
			report.CodexHook = result.HookPath
		} else if agent.ID == "claude" {
			report.ClaudeHook = result.HookPath
		} else if agent.ID == "kimi" {
			report.KimiHook = result.HookPath
		} else if agent.ID == "opencode" {
			report.OpenCodeHook = result.HookPath
		} else if agent.ID == "qoder" {
			report.QoderHook = result.HookPath
		} else if agent.ID == "qoder-work" {
			report.QoderWorkHook = result.HookPath
		} else {
			report.TraeHook = result.HookPath
		}
		if _, err := hooks.Verify(); err != nil {
			return fmt.Errorf("验证 %s 钩子失败：%w", displayName, err)
		}
		setup.printf("%s 通知钩子已安装并验证（%s）", displayName, result.HookPath)
		if agent.ID == "codex" {
			setup.printf("Codex 首次运行或 Stop 钩子变化后，请在 `/hooks` 中审核并信任 AgentBell 钩子，然后新建任务")
		} else if agent.ID == "claude" {
			setup.printf("Claude Code CLI 与 Desktop 本地会话共享用户级 settings Hook")
		} else if agent.ID == "kimi" {
			setup.printf("Kimi Code 仅在会话启动时加载钩子；请关闭旧会话并启动一个新会话")
		} else if agent.ID == "opencode" {
			setup.printf("OpenCode 在下次启动时自动加载全局插件")
		} else if agent.ID == "qoder" {
			setup.printf("Qoder CLI 与 IDE 共享用户级 settings Hook")
		} else if agent.ID == "qoder-work" {
			setup.printf("QoderWork 不支持 Hook 热更新；请完全退出并重新启动 QoderWork")
		} else {
			setup.printf("请在 TRAE Hooks 设置中允许 AgentBell Hook 自动在本地运行，再完成一个新任务")
		}
	}
	return nil
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
