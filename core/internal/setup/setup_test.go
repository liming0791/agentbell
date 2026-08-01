package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/liming0791/agentbell/core/internal/adapter"
	"github.com/liming0791/agentbell/core/internal/config"
)

type capturedCall struct {
	name string
	args []string
}

type fakeRunner struct {
	captureResponses map[string][]byte
	captureErrors    map[string]error
	captureCalls     []capturedCall
	interactiveCalls []capturedCall
	interactiveErr   error
	onCapture        func(call capturedCall)
}

func callKey(name string, args []string) string {
	return name + " " + strings.Join(args, " ")
}

func (runner *fakeRunner) Capture(_ context.Context, name string, args ...string) ([]byte, error) {
	call := capturedCall{name: name, args: args}
	runner.captureCalls = append(runner.captureCalls, call)
	if runner.onCapture != nil {
		runner.onCapture(call)
	}
	key := callKey(name, args)
	if err, ok := runner.captureErrors[key]; ok {
		return nil, err
	}
	if output, ok := runner.captureResponses[key]; ok {
		return output, nil
	}
	return nil, nil
}

func (runner *fakeRunner) Interactive(_ context.Context, name string, args ...string) error {
	runner.interactiveCalls = append(runner.interactiveCalls, capturedCall{name: name, args: args})
	return runner.interactiveErr
}

type fakePrompter struct {
	confirms   []bool
	inputs     []string
	selects    []int
	confirmErr error
}

func (prompter *fakePrompter) Confirm(string) (bool, error) {
	if prompter.confirmErr != nil {
		return false, prompter.confirmErr
	}
	if len(prompter.confirms) == 0 {
		return false, errors.New("unexpected Confirm call")
	}
	answer := prompter.confirms[0]
	prompter.confirms = prompter.confirms[1:]
	return answer, nil
}

func (prompter *fakePrompter) Input(string) (string, error) {
	if len(prompter.inputs) == 0 {
		return "", errors.New("unexpected Input call")
	}
	answer := prompter.inputs[0]
	prompter.inputs = prompter.inputs[1:]
	return answer, nil
}

func (prompter *fakePrompter) Select(_ string, options []string) (int, error) {
	if len(prompter.selects) == 0 {
		return -1, errors.New("unexpected Select call")
	}
	answer := prompter.selects[0]
	prompter.selects = prompter.selects[1:]
	if answer < 0 || answer >= len(options) {
		return -1, errors.New("select out of range")
	}
	return answer, nil
}

type fakeHookAdapter struct {
	id        string
	hookPath  string
	installed bool
	verifyErr error
}

func (fake *fakeHookAdapter) Install(bool) (adapter.AdapterResult, error) {
	fake.installed = true
	return adapter.AdapterResult{Adapter: fake.id, Installed: true, HookPath: fake.hookPath}, nil
}

func (fake *fakeHookAdapter) Verify() (adapter.AdapterResult, error) {
	if fake.verifyErr != nil {
		return adapter.AdapterResult{}, fake.verifyErr
	}
	return adapter.AdapterResult{Adapter: fake.id, Installed: true}, nil
}

// newFixture builds a fully-faked Setup: codex + kimi agents detected,
// lark-cli present, configured and authorized, search returns one chat.
func newFixture(t *testing.T) (*Setup, *fakeRunner, *fakePrompter, *fakeHookAdapter, *fakeHookAdapter) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(filepath.Join(home, ".kimi"), 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{
		captureResponses: map[string][]byte{
			"lark-cli --version":                   []byte("lark-cli version 1.0.81\n"),
			"lark-cli config show":                 []byte(`{"appId":"cli_test"}`),
			"lark-cli auth status --verify --json": []byte(`{"identity":"user","identities":{"user":{"status":"ready","available":true,"verified":true,"openId":"ou_setup_user"}}}`),
			`lark-cli im +chat-search --query 通知 --search-types private,public_joined,external --as user --format json`: []byte(
				`{"ok":true,"data":{"chats":[{"chat_id":"oc_found","name":"运维通知"}]}}`,
			),
			`lark-cli im +chat-search --query 通知 --search-types private,public_joined,external --as bot --format json`: []byte(
				`{"ok":true,"data":{"chats":[{"chat_id":"oc_found","name":"运维通知"}]}}`,
			),
			`lark-cli im +chat-create --name AgentBell 通知 --chat-mode group --type private --users ou_setup_user --as bot --format json`: []byte(
				`{"ok":true,"data":{"chat_id":"oc_created"}}`,
			),
		},
		captureErrors: map[string]error{},
	}
	prompter := &fakePrompter{
		confirms: []bool{true, true}, // codex hook install, kimi hook install
		selects:  []int{0, 0},        // search path, first chat
		inputs:   []string{"通知"},
	}
	codex := &fakeHookAdapter{id: "codex", hookPath: "/fake/hooks.json"}
	kimi := &fakeHookAdapter{id: "kimi-code", hookPath: "/fake/config.toml"}
	binaryDir := filepath.Join(root, "bin")
	setup := &Setup{
		Runner:   runner,
		Prompter: prompter,
		LookPath: func(name string) (string, error) {
			if name == "codex" || name == "lark-cli" {
				return filepath.Join(binaryDir, name), nil
			}
			return "", errors.New("not found")
		},
		Now:        func() time.Time { return time.Date(2026, 7, 25, 8, 0, 0, 0, time.UTC) },
		HomeDir:    home,
		ConfigFile: filepath.Join(root, "config", "config.json"),
		StateDir:   filepath.Join(root, "state"),
		Out:        &bytes.Buffer{},
		Stat: func(path string) (os.FileInfo, error) {
			if strings.HasPrefix(
				path,
				filepath.Join(string(filepath.Separator), "Applications")+string(filepath.Separator),
			) {
				return nil, os.ErrNotExist
			}
			return os.Stat(path)
		},
		NewCodexAdapter: func() (hookAdapter, error) {
			return codex, nil
		},
		NewKimiAdapter: func() (hookAdapter, error) {
			return kimi, nil
		},
	}
	return setup, runner, prompter, codex, kimi
}

func TestSetupHappyPathSearchChat(t *testing.T) {
	setup, runner, _, codex, kimi := newFixture(t)
	report, err := setup.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Channel == nil || report.Channel.ChatID != "oc_found" {
		t.Fatalf("unexpected channel: %#v", report.Channel)
	}
	if !codex.installed || report.CodexHook == "" {
		t.Fatal("codex adapter was not installed")
	}
	if !kimi.installed || report.KimiHook == "" {
		t.Fatal("kimi adapter was not installed")
	}
	loaded, err := config.Load(setup.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.DefaultChannel != "feishu" || len(loaded.Channels) != 1 ||
		loaded.Channels[0].ChatID != "oc_found" || loaded.Channels[0].Name != "运维通知" {
		t.Fatalf("config mismatch: %#v", loaded)
	}
	expectedLarkCLIPath, err := setup.LookPath("lark-cli")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.LarkCLIPath != expectedLarkCLIPath {
		t.Fatalf("absolute lark-cli path was not persisted: %#v", loaded)
	}
	if loaded.Notifications.PrivacyLevel != "metadata-only" ||
		len(loaded.Notifications.Events) != 4 {
		t.Fatalf("notification defaults mismatch: %#v", loaded.Notifications)
	}
	if len(runner.interactiveCalls) != 0 {
		t.Fatalf("no interactive commands expected: %#v", runner.interactiveCalls)
	}
	out := setup.Out.(*bytes.Buffer).String()
	if !strings.Contains(out, "设置完成") || !strings.Contains(out, "agentbell test") {
		t.Fatalf("missing next steps in output: %s", out)
	}
}

func TestSetupRequiresUserAuthWhenOnlyBotIsAuthorized(t *testing.T) {
	setup, runner, prompter, _, _ := newFixture(t)
	runner.captureResponses["lark-cli auth status --verify --json"] = []byte(
		`{"identity":"bot","identities":{"user":{"status":"missing","available":false},"bot":{"status":"ready","available":true}}}`,
	)
	prompter.confirms = []bool{false}

	_, err := setup.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "飞书用户授权") {
		t.Fatalf("expected user authorization error, got %v", err)
	}
	if len(runner.interactiveCalls) != 0 {
		t.Fatalf("declined login must not run an interactive command: %#v", runner.interactiveCalls)
	}
}

func TestVerifiedUserAuthSupportsLegacyAndRejectsFailedVerification(t *testing.T) {
	setup, runner, _, _, _ := newFixture(t)
	runner.captureResponses["lark-cli auth status --verify --json"] = []byte(
		`{"identity":"user","verified":true,"userOpenId":"ou_legacy_user"}`,
	)
	status, err := setup.verifiedUserAuth(context.Background())
	if err != nil || status.UserOpenID != "ou_legacy_user" {
		t.Fatalf("legacy auth status was not accepted: status=%#v err=%v", status, err)
	}

	verified := false
	failedStatus := authStatus{Identity: "user"}
	failedStatus.Identities.User = authIdentityStatus{
		Status:    "invalid",
		Available: true,
		Verified:  &verified,
		OpenID:    "ou_unverified_user",
	}
	encoded, err := json.Marshal(failedStatus)
	if err != nil {
		t.Fatal(err)
	}
	runner.captureResponses["lark-cli auth status --verify --json"] = encoded
	if _, err := setup.verifiedUserAuth(context.Background()); err == nil {
		t.Fatal("a user whose remote verification failed must be rejected")
	}
}

func TestSetupSearchOnlyOffersChatsSharedByUserAndBot(t *testing.T) {
	setup, runner, _, _, _ := newFixture(t)
	runner.captureResponses[`lark-cli im +chat-search --query 通知 --search-types private,public_joined,external --as user --format json`] = []byte(
		`{"ok":true,"data":{"chats":[{"chat_id":"oc_user_only","name":"用户群"},{"chat_id":"oc_shared","name":"共同群"}]}}`,
	)
	runner.captureResponses[`lark-cli im +chat-search --query 通知 --search-types private,public_joined,external --as bot --format json`] = []byte(
		`{"ok":true,"data":{"chats":[{"chat_id":"oc_bot_only","name":"机器人群"},{"chat_id":"oc_shared","name":"共同群"}]}}`,
	)

	report, err := setup.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Channel == nil || report.Channel.ChatID != "oc_shared" {
		t.Fatalf("setup selected a chat that was not shared: %#v", report.Channel)
	}
}

func TestSetupSearchErrorDoesNotExposeUserIdentity(t *testing.T) {
	setup, runner, _, _, _ := newFixture(t)
	key := `lark-cli im +chat-search --query 通知 --search-types private,public_joined,external --as user --format json`
	runner.captureErrors[key] = errors.New("authentication failed for ou_sensitive_user")

	_, err := setup.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "auth login --domain im") ||
		strings.Contains(err.Error(), "ou_sensitive_user") {
		t.Fatalf("expected redacted, actionable search error, got %v", err)
	}
}

func TestSetupDefaultAdaptersUseSelectedRuntime(t *testing.T) {
	root := t.TempDir()
	coreExecutable := filepath.Join(root, "bin", "0.3.0", "agentbell")
	bridgeExecutable := filepath.Join(
		root,
		"bin",
		"bridge",
		"v1",
		"agentbell-bridge",
	)
	t.Setenv("CODEX_HOME", filepath.Join(root, ".codex"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(root, ".claude"))
	t.Setenv("KIMI_CODE_HOME", filepath.Join(root, ".kimi-code"))
	setup := &Setup{
		HomeDir:          root,
		StateDir:         filepath.Join(root, "state"),
		CoreExecutable:   coreExecutable,
		BridgeExecutable: bridgeExecutable,
		ActiveGeneration: 23,
	}
	if err := setup.resolve(); err != nil {
		t.Fatal(err)
	}

	codexValue, err := setup.NewCodexAdapter()
	if err != nil {
		t.Fatal(err)
	}
	codex := codexValue.(*adapter.CodexAdapter)
	if codex.Executable != coreExecutable ||
		codex.BridgeExecutable != bridgeExecutable ||
		codex.ActiveGeneration != 23 {
		t.Fatalf(
			"codex runtime = (%q, %q, %d)",
			codex.Executable,
			codex.BridgeExecutable,
			codex.ActiveGeneration,
		)
	}

	openCodeValue, err := setup.NewOpenCodeAdapter()
	if err != nil {
		t.Fatal(err)
	}
	if openCodeValue.(*adapter.OpenCodeAdapter).Executable != coreExecutable {
		t.Fatal("OpenCode adapter did not use the selected active Core")
	}
}

func TestSetupInstallsBackgroundServiceOnMacOS(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("LaunchAgent setup is macOS-specific")
	}
	setup, _, prompter, _, _ := newFixture(t)
	prompter.confirms = []bool{true, true, true}
	called := false
	setup.InstallService = func(context.Context) (string, error) {
		called = true
		return "/tmp/com.agentbell.service.plist", nil
	}
	report, err := setup.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !called || report.Service != "/tmp/com.agentbell.service.plist" {
		t.Fatalf("background service was not installed: %#v", report)
	}
	if !strings.Contains(setup.Out.(*bytes.Buffer).String(), "service status") {
		t.Fatal("next steps did not reflect the installed service")
	}
}

func TestSetupCreateChatInstallsKimiHooks(t *testing.T) {
	setup, _, prompter, _, kimi := newFixture(t)
	prompter.selects = []int{1}    // create new chat
	prompter.inputs = []string{""} // default name
	report, err := setup.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Channel.ChatID != "oc_created" || report.Channel.Name != "AgentBell 通知" {
		t.Fatalf("unexpected channel: %#v", report.Channel)
	}
	if !kimi.installed || report.KimiHook == "" {
		t.Fatal("kimi adapter was not installed")
	}
	out := setup.Out.(*bytes.Buffer).String()
	if !strings.Contains(out, "Kimi Code 通知钩子已安装并验证") {
		t.Fatalf("missing kimi install notice: %s", out)
	}
}

func TestSetupCanPauseForOneTimeBinding(t *testing.T) {
	setup, _, prompter, codex, kimi := newFixture(t)
	prompter.selects = []int{2, 1} // binding path, user identity
	prompter.inputs = []string{"Team notifications"}
	called := false
	setup.CreateBinding = func(
		_ context.Context,
		request BindingRequest,
	) (BindingResult, error) {
		called = true
		if request.ChannelName != "Team notifications" ||
			request.Identity != "user" ||
			request.LarkCLIPath == "" {
			t.Fatalf("binding request = %#v", request)
		}
		return BindingResult{
			Code:        "AGB-01234-56789-ABCDE-FGHJK",
			ChannelName: request.ChannelName,
			Identity:    request.Identity,
			ExpiresAt:   time.Now().Add(10 * time.Minute),
		}, nil
	}

	report, err := setup.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !called || report.Binding == nil ||
		report.Binding.Code != "AGB-01234-56789-ABCDE-FGHJK" {
		t.Fatalf("pending binding report = %#v", report)
	}
	if codex.installed || kimi.installed || report.Service != "" {
		t.Fatal("pending binding setup installed Hooks or service before a channel existed")
	}
	if _, err := os.Stat(setup.ConfigFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending binding setup wrote config: %v", err)
	}
	out := setup.Out.(*bytes.Buffer).String()
	if !strings.Contains(out, report.Binding.Code) ||
		!strings.Contains(out, "agentbell bind complete --code-stdin") {
		t.Fatalf("missing binding continuation guidance: %s", out)
	}
}

func TestSetupInstallsClaudeHooksForCLIAndDesktop(t *testing.T) {
	setup, _, prompter, _, _ := newFixture(t)
	if err := os.MkdirAll(filepath.Join(setup.HomeDir, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	claude := &fakeHookAdapter{
		id:       "claude-code",
		hookPath: filepath.Join(setup.HomeDir, ".claude", "settings.json"),
	}
	setup.NewClaudeAdapter = func() (hookAdapter, error) {
		return claude, nil
	}
	// detected order: Codex, Claude Code, Kimi Code.
	prompter.confirms = []bool{true, true, true}

	report, err := setup.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !claude.installed || report.ClaudeHook != claude.hookPath {
		t.Fatalf("Claude adapter was not installed: %#v", report)
	}
	out := setup.Out.(*bytes.Buffer).String()
	if !strings.Contains(out, "Claude Code CLI 与 Desktop 本地会话共享") {
		t.Fatalf("missing shared-surface guidance: %s", out)
	}
}

func TestSetupOffersManagedServiceOnLinuxAndWindows(t *testing.T) {
	for _, platform := range []string{"linux", "windows"} {
		t.Run(platform, func(t *testing.T) {
			setup, _, prompter, _, _ := newFixture(t)
			setup.GOOS = platform
			prompter.confirms = []bool{true, true, true}
			called := false
			setup.InstallService = func(context.Context) (string, error) {
				called = true
				return "service-definition", nil
			}
			report, err := setup.Run(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !called || report.Service != "service-definition" {
				t.Fatalf("service was not installed on %s: %#v", platform, report)
			}
			if !strings.Contains(setup.Out.(*bytes.Buffer).String(), "service status") {
				t.Fatalf("missing service next step on %s", platform)
			}
		})
	}
}

func TestSetupDeclineKimiHooks(t *testing.T) {
	setup, _, prompter, _, kimi := newFixture(t)
	prompter.confirms = []bool{true, false} // codex yes, kimi no
	report, err := setup.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if kimi.installed || report.KimiHook != "" {
		t.Fatal("declined kimi adapter must not be installed")
	}
	out := setup.Out.(*bytes.Buffer).String()
	if !strings.Contains(out, "已跳过 Kimi Code 钩子安装，可稍后运行 `agentbell adapter install kimi-code`") {
		t.Fatalf("missing kimi skip notice: %s", out)
	}
}

func TestSetupKimiVerifyFailure(t *testing.T) {
	setup, _, _, _, kimi := newFixture(t)
	kimi.verifyErr = errors.New("hooks incomplete")
	_, err := setup.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "验证 Kimi Code 钩子失败") {
		t.Fatalf("expected kimi verify error, got %v", err)
	}
}

func TestSetupCreateChatInvitesUserAsBot(t *testing.T) {
	setup, runner, prompter, _, _ := newFixture(t)
	prompter.selects = []int{1}    // create new chat
	prompter.inputs = []string{""} // default name
	runner.captureResponses["lark-cli auth status --verify --json"] = []byte(
		`{"identity":"user","identities":{"user":{"status":"ready","available":true,"verified":true,"openId":"ou_user1"}}}`)
	runner.captureResponses[`lark-cli im +chat-create --name AgentBell 通知 --chat-mode group --type private --users ou_user1 --as bot --format json`] = []byte(
		`{"ok":true,"data":{"chat_id":"oc_created_with_user"}}`)
	report, err := setup.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Channel.ChatID != "oc_created_with_user" {
		t.Fatalf("unexpected channel: %#v", report.Channel)
	}
}

func TestSetupDryRun(t *testing.T) {
	setup, runner, prompter, codex, _ := newFixture(t)
	setup.DryRun = true
	report, err := setup.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !report.DryRun || report.Channel != nil {
		t.Fatalf("dry-run report mismatch: %#v", report)
	}
	if len(runner.captureCalls) != 0 || len(runner.interactiveCalls) != 0 {
		t.Fatal("dry-run must not execute any subprocess")
	}
	if len(prompter.confirms) != 2 || len(prompter.inputs) != 1 {
		t.Fatal("dry-run must not prompt")
	}
	if codex.installed {
		t.Fatal("dry-run must not install adapters")
	}
	if _, err := os.Stat(setup.ConfigFile); !os.IsNotExist(err) {
		t.Fatal("dry-run must not write the config")
	}
	out := setup.Out.(*bytes.Buffer).String()
	if !strings.Contains(out, "[dry-run]") || !strings.Contains(out, "kimi（config-dir）") {
		t.Fatalf("plan output incomplete: %s", out)
	}
}

func TestSetupDetectsDesktopConfigFromOfficialEnvironmentOverrides(t *testing.T) {
	setup, _, _, _, _ := newFixture(t)
	customClaudeHome := filepath.Join(filepath.Dir(setup.HomeDir), "custom-claude")
	if err := os.MkdirAll(customClaudeHome, 0o700); err != nil {
		t.Fatal(err)
	}
	setup.Getenv = func(name string) string {
		if name == "CLAUDE_CONFIG_DIR" {
			return customClaudeHome
		}
		return ""
	}
	if err := setup.resolve(); err != nil {
		t.Fatal(err)
	}
	for _, status := range setup.detectAgents() {
		if status.ID == "claude" {
			if !status.Detected || status.Source != "config-env" {
				t.Fatalf("custom Claude config was not detected: %#v", status)
			}
			return
		}
	}
	t.Fatal("Claude agent status is missing")
}

func TestSetupM15DetectionRespectsDesktopPlatforms(t *testing.T) {
	setup, _, _, _, _ := newFixture(t)
	for _, directory := range []string{".qoderwork", ".trae"} {
		if err := os.MkdirAll(filepath.Join(setup.HomeDir, directory), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	setup.GOOS = "linux"
	if err := setup.resolve(); err != nil {
		t.Fatal(err)
	}
	for _, status := range setup.detectAgents() {
		if (status.ID == "qoder-work" || status.ID == "trae") && status.Detected {
			t.Fatalf("%s must not be offered on Linux: %#v", status.ID, status)
		}
	}
	setup.GOOS = "windows"
	found := 0
	for _, status := range setup.detectAgents() {
		if status.ID == "qoder-work" || status.ID == "trae" {
			if !status.Detected || status.Source != "config-dir" {
				t.Fatalf("%s was not detected on Windows: %#v", status.ID, status)
			}
			found++
		}
	}
	if found != 2 {
		t.Fatalf("missing M1.5 agent statuses: %d", found)
	}
}

func TestSetupM15DetectionFindsDesktopApplications(t *testing.T) {
	setup, _, _, _, _ := newFixture(t)
	setup.GOOS = "darwin"
	setup.LookPath = func(string) (string, error) {
		return "", errors.New("not found")
	}
	info, err := os.Stat(setup.HomeDir)
	if err != nil {
		t.Fatal(err)
	}
	setup.Stat = func(path string) (os.FileInfo, error) {
		if path == filepath.Join(string(filepath.Separator), "Applications", "QoderWork CN.app") ||
			path == filepath.Join(string(filepath.Separator), "Applications", "Trae CN.app") {
			return info, nil
		}
		return nil, os.ErrNotExist
	}
	if err := setup.resolve(); err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, status := range setup.detectAgents() {
		if status.ID == "qoder-work" || status.ID == "trae" {
			if !status.Detected || status.Source != "application" {
				t.Fatalf("%s system application was not detected: %#v", status.ID, status)
			}
			found++
		}
	}
	if found != 2 {
		t.Fatalf("missing M1.5 application statuses: %d", found)
	}
}

func TestDesktopInstallPathsUsesWindowsApplicationRoots(t *testing.T) {
	root := t.TempDir()
	getenv := func(name string) string {
		if name == "LOCALAPPDATA" {
			return filepath.Join(root, "Local")
		}
		if name == "ProgramFiles" {
			return filepath.Join(root, "Programs")
		}
		return ""
	}
	for _, testCase := range []struct {
		agentID string
		appName string
	}{
		{agentID: "qoder-work", appName: "QoderWork.exe"},
		{agentID: "trae", appName: "TRAE.exe"},
	} {
		paths := desktopInstallPaths("windows", getenv, testCase.agentID)
		if len(paths) != 3 || !strings.HasSuffix(paths[0], testCase.appName) ||
			!strings.HasPrefix(paths[0], getenv("LOCALAPPDATA")) ||
			!strings.HasPrefix(paths[2], getenv("ProgramFiles")) {
			t.Fatalf("unexpected %s install paths: %#v", testCase.agentID, paths)
		}
	}
	if paths := desktopInstallPaths("linux", getenv, "trae"); paths != nil {
		t.Fatalf("unsupported platform returned install paths: %#v", paths)
	}
	if paths := desktopInstallPaths("darwin", getenv, "unknown"); paths != nil {
		t.Fatalf("unknown product returned install paths: %#v", paths)
	}
	if paths := desktopInstallPaths("darwin", getenv, "qoder-work"); len(paths) != 2 ||
		!strings.HasSuffix(paths[1], "QoderWork CN.app") {
		t.Fatalf("QoderWork CN application path is missing: %#v", paths)
	}
	if paths := desktopInstallPaths("darwin", getenv, "trae"); len(paths) != 2 ||
		!strings.HasSuffix(paths[1], "Trae CN.app") {
		t.Fatalf("TRAE CN application path is missing: %#v", paths)
	}
	if paths := desktopInstallPaths("windows", func(string) string { return "" }, "trae"); len(paths) != 0 {
		t.Fatalf("unset Windows roots returned relative install paths: %#v", paths)
	}
}

func TestSetupInstallsMissingLarkCLI(t *testing.T) {
	setup, runner, prompter, _, _ := newFixture(t)
	installed := false
	binaryDir := filepath.Join(t.TempDir(), "bin")
	setup.LookPath = func(name string) (string, error) {
		if name == "lark-cli" && !installed {
			return "", errors.New("not found")
		}
		if name == "lark-cli" || name == "codex" {
			return filepath.Join(binaryDir, name), nil
		}
		return "", errors.New("not found")
	}
	// confirms: install lark-cli? yes; codex hooks? yes; kimi hooks? yes
	prompter.confirms = []bool{true, true, true}
	// After the interactive install, lark-cli becomes available.
	setup.Runner = &larkInstallRunner{fakeRunner: runner, markInstalled: func() { installed = true }}
	report, err := setup.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !report.LarkCLI.Installed {
		t.Fatal("lark-cli should be installed")
	}
	calls := runner.interactiveCalls
	if len(calls) != 1 || calls[0].name != "npx" {
		t.Fatalf("expected npx install call, got %#v", calls)
	}
}

type larkInstallRunner struct {
	*fakeRunner
	markInstalled func()
}

func (runner *larkInstallRunner) Interactive(ctx context.Context, name string, args ...string) error {
	if name == "npx" {
		runner.markInstalled()
	}
	return runner.fakeRunner.Interactive(ctx, name, args...)
}

func TestSetupDeclineLarkCLIInstall(t *testing.T) {
	setup, _, prompter, _, _ := newFixture(t)
	setup.LookPath = func(string) (string, error) { return "", errors.New("not found") }
	prompter.confirms = []bool{false}
	_, err := setup.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "需要 lark-cli") {
		t.Fatalf("expected lark-cli requirement error, got %v", err)
	}
}

func TestSetupConfigInitAndAuthLogin(t *testing.T) {
	setup, runner, prompter, _, _ := newFixture(t)
	configured := false
	authorized := false
	setup.Runner = &statefulRunner{
		fakeRunner: runner,
		configured: &configured,
		authorized: &authorized,
	}
	prompter.confirms = []bool{true, true, true, true} // config init, auth login, codex hooks, kimi hooks
	_, err := setup.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var sawInit, sawLogin bool
	for _, call := range runner.interactiveCalls {
		if callKey(call.name, call.args) == "lark-cli config init" {
			sawInit = true
		}
		if callKey(call.name, call.args) == "lark-cli auth login --domain im" {
			sawLogin = true
		}
	}
	if !sawInit || !sawLogin {
		t.Fatalf("expected config init and auth login, got %#v", runner.interactiveCalls)
	}
}

type statefulRunner struct {
	*fakeRunner
	configured *bool
	authorized *bool
}

func (runner *statefulRunner) Capture(ctx context.Context, name string, args ...string) ([]byte, error) {
	key := callKey(name, args)
	if key == "lark-cli config show" && !*runner.configured {
		return nil, errors.New("no config")
	}
	if key == "lark-cli auth status --verify --json" && !*runner.authorized {
		return nil, errors.New("not logged in")
	}
	return runner.fakeRunner.Capture(ctx, name, args...)
}

func (runner *statefulRunner) Interactive(ctx context.Context, name string, args ...string) error {
	key := callKey(name, args)
	if key == "lark-cli config init" {
		*runner.configured = true
	}
	if key == "lark-cli auth login --domain im" {
		*runner.authorized = true
	}
	return runner.fakeRunner.Interactive(ctx, name, args...)
}

func TestSetupMergesExistingConfig(t *testing.T) {
	setup, _, _, _, _ := newFixture(t)
	existing := &config.Config{
		DefaultChannel: "team",
		Notifications: config.Notifications{
			Events:       []string{"task.completed"},
			PrivacyLevel: "summary",
		},
		Channels: []config.Channel{
			{ID: "team", Name: "团队群", Type: "feishu", ChatID: "oc_team", As: "user"},
		},
	}
	if err := config.Save(setup.ConfigFile, existing); err != nil {
		t.Fatal(err)
	}
	report, err := setup.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Backup == "" {
		t.Fatal("expected a backup path")
	}
	if _, err := os.Stat(report.Backup); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	loaded, err := config.Load(setup.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.DefaultChannel != "team" || len(loaded.Channels) != 2 {
		t.Fatalf("merge mismatch: %#v", loaded)
	}
	if loaded.Notifications.PrivacyLevel != "summary" {
		t.Fatalf("existing notification settings must be preserved: %#v", loaded.Notifications)
	}

	// Second run stays idempotent: same two channels, updated in place.
	setup.Prompter = &fakePrompter{
		confirms: []bool{true, true},
		selects:  []int{0, 0},
		inputs:   []string{"通知"},
	}
	if _, err := setup.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	reloaded, err := config.Load(setup.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Channels) != 2 {
		t.Fatalf("re-run must be idempotent: %#v", reloaded.Channels)
	}
}

func TestSetupRejectsCorruptExistingConfig(t *testing.T) {
	setup, _, _, _, _ := newFixture(t)
	if err := os.MkdirAll(filepath.Dir(setup.ConfigFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(setup.ConfigFile, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := setup.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "无法解析") {
		t.Fatalf("expected corrupt-config error, got %v", err)
	}
	raw, _ := os.ReadFile(setup.ConfigFile)
	if string(raw) != "{broken" {
		t.Fatal("corrupt config must not be overwritten")
	}
}

func TestSetupEmptySearchResult(t *testing.T) {
	setup, runner, _, _, _ := newFixture(t)
	runner.captureResponses[`lark-cli im +chat-search --query 通知 --search-types private,public_joined,external --as user --format json`] = []byte(
		`{"ok":true,"data":{"chats":null}}`,
	)
	_, err := setup.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "没有找到") {
		t.Fatalf("expected empty-search error, got %v", err)
	}
}

func TestSetupCodexVerifyFailure(t *testing.T) {
	setup, _, _, codex, _ := newFixture(t)
	codex.verifyErr = errors.New("hooks incomplete")
	_, err := setup.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "验证 Codex 钩子失败") {
		t.Fatalf("expected verify error, got %v", err)
	}
}

func TestStdioPrompter(t *testing.T) {
	prompter := NewStdioPrompter(strings.NewReader("y\nn\n\nhello world\n2\n"), &bytes.Buffer{})
	if ok, err := prompter.Confirm("q1"); err != nil || !ok {
		t.Fatalf("confirm y: %v %v", ok, err)
	}
	if ok, err := prompter.Confirm("q2"); err != nil || ok {
		t.Fatalf("confirm n: %v %v", ok, err)
	}
	if ok, err := prompter.Confirm("q3"); err != nil || !ok {
		t.Fatalf("confirm default: %v %v", ok, err)
	}
	if value, err := prompter.Input("q4"); err != nil || value != "hello world" {
		t.Fatalf("input: %q %v", value, err)
	}
	if choice, err := prompter.Select("q5", []string{"a", "b"}); err != nil || choice != 1 {
		t.Fatalf("select: %d %v", choice, err)
	}
	if _, err := prompter.Confirm("q6"); err == nil {
		t.Fatal("expected EOF error")
	}

	bad := NewStdioPrompter(strings.NewReader("maybe\n9\n"), &bytes.Buffer{})
	if _, err := bad.Confirm("q"); err == nil {
		t.Fatal("expected invalid confirm error")
	}
	if _, err := bad.Select("q", []string{"a"}); err == nil {
		t.Fatal("expected out-of-range select error")
	}
	if _, err := bad.Select("q", nil); err == nil {
		t.Fatal("expected empty-options error")
	}
}

func TestExecRunner(t *testing.T) {
	var stdout bytes.Buffer
	runner := ExecRunner{Stdout: &stdout, Stderr: &bytes.Buffer{}, Stdin: strings.NewReader("")}
	output, err := runner.Capture(context.Background(), "echo", "hello")
	if err != nil || strings.TrimSpace(string(output)) != "hello" {
		t.Fatalf("capture: %q %v", output, err)
	}
	if _, err := runner.Capture(context.Background(), "sh", "-c", "echo boom >&2; exit 1"); err == nil ||
		!strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected stderr in error, got %v", err)
	}
	if err := runner.Interactive(context.Background(), "echo", "hi"); err != nil {
		t.Fatalf("interactive: %v", err)
	}
	if !strings.Contains(stdout.String(), "hi") {
		t.Fatalf("interactive output not passed through: %q", stdout.String())
	}
}

func TestSetupOpenCodeQoderAndM15Adapters(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(filepath.Join(home, ".config", "opencode"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".qoder"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".qoderwork"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".trae"), 0o700); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{
		captureResponses: map[string][]byte{
			"lark-cli --version":                   []byte("lark-cli version 1.0.81\n"),
			"lark-cli config show":                 []byte(`{"appId":"cli_test"}`),
			"lark-cli auth status --verify --json": []byte(`{"identity":"user","identities":{"user":{"status":"ready","available":true,"verified":true,"openId":"ou_setup_user"}}}`),
			`lark-cli im +chat-search --query 通知 --search-types private,public_joined,external --as user --format json`: []byte(
				`{"ok":true,"data":{"chats":[{"chat_id":"oc_found","name":"运维通知"}]}}`,
			),
			`lark-cli im +chat-search --query 通知 --search-types private,public_joined,external --as bot --format json`: []byte(
				`{"ok":true,"data":{"chats":[{"chat_id":"oc_found","name":"运维通知"}]}}`,
			),
		},
		captureErrors: map[string]error{},
	}
	prompter := &fakePrompter{
		confirms: []bool{true, true, true, true},
		selects:  []int{0, 0},
		inputs:   []string{"通知"},
	}
	opencode := &fakeHookAdapter{id: "opencode", hookPath: "/fake/plugins/agentbell.js"}
	qoder := &fakeHookAdapter{id: "qoder", hookPath: "/fake/settings.json"}
	qoderWork := &fakeHookAdapter{id: "qoder-work", hookPath: "/fake/qoderwork/settings.json"}
	trae := &fakeHookAdapter{id: "trae", hookPath: "/fake/trae/hooks.json"}
	binaryDir := filepath.Join(root, "bin")
	setup := &Setup{
		Runner:   runner,
		Prompter: prompter,
		LookPath: func(name string) (string, error) {
			if name == "lark-cli" {
				return filepath.Join(binaryDir, name), nil
			}
			return "", errors.New("not found")
		},
		Now:        func() time.Time { return time.Date(2026, 7, 26, 8, 0, 0, 0, time.UTC) },
		HomeDir:    home,
		ConfigFile: filepath.Join(root, "config", "config.json"),
		StateDir:   filepath.Join(root, "state"),
		GOOS:       "darwin",
		Out:        &bytes.Buffer{},
		NewOpenCodeAdapter: func() (hookAdapter, error) {
			return opencode, nil
		},
		NewQoderAdapter: func() (hookAdapter, error) {
			return qoder, nil
		},
		NewQoderWorkAdapter: func() (hookAdapter, error) {
			return qoderWork, nil
		},
		NewTraeAdapter: func() (hookAdapter, error) {
			return trae, nil
		},
	}
	report, err := setup.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !opencode.installed || report.OpenCodeHook == "" {
		t.Fatal("opencode adapter was not installed")
	}
	if !qoder.installed || report.QoderHook == "" {
		t.Fatal("qoder adapter was not installed")
	}
	if !qoderWork.installed || report.QoderWorkHook == "" {
		t.Fatal("qoder-work adapter was not installed")
	}
	if !trae.installed || report.TraeHook == "" {
		t.Fatal("TRAE adapter was not installed")
	}
	out := setup.Out.(*bytes.Buffer).String()
	if !strings.Contains(out, "OpenCode 通知钩子已安装并验证") {
		t.Fatalf("missing opencode install notice: %s", out)
	}
	if !strings.Contains(out, "Qoder 通知钩子已安装并验证") {
		t.Fatalf("missing qoder install notice: %s", out)
	}
	if !strings.Contains(out, "OpenCode 在下次启动时自动加载全局插件") {
		t.Fatalf("missing opencode guidance: %s", out)
	}
	if !strings.Contains(out, "Qoder CLI 与 IDE 共享用户级 settings Hook") {
		t.Fatalf("missing qoder guidance: %s", out)
	}
	if !strings.Contains(out, "QoderWork 不支持 Hook 热更新") {
		t.Fatalf("missing QoderWork restart guidance: %s", out)
	}
	if !strings.Contains(out, "允许 AgentBell Hook 自动在本地运行") {
		t.Fatalf("missing TRAE execution guidance: %s", out)
	}
}
