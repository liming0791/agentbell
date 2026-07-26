package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/liming0791/agentbell/core/internal/adapter"
	"github.com/liming0791/agentbell/core/internal/config"
	"github.com/liming0791/agentbell/core/internal/event"
	"github.com/liming0791/agentbell/core/internal/paths"
	"github.com/liming0791/agentbell/core/internal/queue"
	"github.com/liming0791/agentbell/core/internal/service"
	"github.com/liming0791/agentbell/core/internal/setup"
	"github.com/liming0791/agentbell/core/internal/transport"
	"github.com/liming0791/agentbell/core/internal/version"
)

const maxInputSize = 256 * 1024

var newServiceManager = service.NewManager

func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printHelp(stdout)
		return 0
	}

	var err error
	switch args[0] {
	case "version":
		err = runVersion(args[1:], stdout)
	case "emit":
		err = runEmit(args[1:], stdin, stdout)
	case "service":
		err = runService(args[1:], stdout)
	case "doctor":
		err = runDoctor(args[1:], stdout)
	case "queue":
		err = runQueue(args[1:], stdout)
	case "adapter":
		err = runAdapter(args[1:], stdout)
	case "setup":
		err = runSetup(args[1:], stdin, stdout)
	case "test":
		err = runTest(args[1:], stdout)
	case "uninstall":
		err = runProductUninstall(args[1:], stdout)
	default:
		err = fmt.Errorf("unsupported command %q", args[0])
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func printHelp(writer io.Writer) {
	fmt.Fprintln(writer, `AgentBell

Usage:
  agentbell version [--json]
  agentbell emit --adapter <id> --surface <surface> --runtime <runtime> --stdin [--fail-open]
  agentbell service <run --foreground|install|status|uninstall>
  agentbell doctor [--json]
  agentbell queue list [--state pending|inflight|dead]
  agentbell queue retry <event-id>
  agentbell adapter <detect|plan|install|verify|uninstall|diagnose> <codex|claude-code|kimi-code>
  agentbell adapter uninstall all [--dry-run]
  agentbell uninstall [--dry-run] [--json]
  agentbell setup [--dry-run] [--json]
  agentbell test [--channel <id>] [--json]`)
}

func runVersion(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("version", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	info := version.Current()
	if *asJSON {
		return writeJSON(stdout, info)
	}
	fmt.Fprintf(stdout, "AgentBell %s (%s) %s/%s\n", info.Version, info.Commit, info.Platform, info.Arch)
	return nil
}

func runEmit(args []string, stdin io.Reader, stdout io.Writer) error {
	flags := flag.NewFlagSet("emit", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	adapterID := flags.String("adapter", "", "adapter id")
	surface := flags.String("surface", "", "agent surface")
	runtimeName := flags.String("runtime", "", "runtime location")
	useStdin := flags.Bool("stdin", false, "read hook JSON from stdin")
	failOpen := flags.Bool("fail-open", false, "return success if enqueue fails")
	if err := flags.Parse(args); err != nil {
		return failOpenError(*failOpen, err)
	}
	if *adapterID == "" || *surface == "" || *runtimeName == "" || !*useStdin {
		return failOpenError(*failOpen, errors.New(
			"emit requires --adapter, --surface, --runtime and --stdin",
		))
	}

	raw, err := readLimited(stdin, maxInputSize)
	if err != nil {
		return failOpenError(*failOpen, err)
	}
	now := time.Now()
	notification, err := event.Normalize(*adapterID, *surface, *runtimeName, raw, now)
	if err != nil {
		return failOpenError(*failOpen, err)
	}
	shouldNotify, err := event.ShouldNotify(*adapterID, raw)
	if err != nil {
		return failOpenError(*failOpen, err)
	}
	resolved, err := paths.Resolve()
	if err != nil {
		return failOpenError(*failOpen, err)
	}
	if proofErr := adapter.RecordRuntimeProof(
		resolved.StateDir,
		*adapterID,
		notification.Event,
		now,
	); proofErr != nil && os.Getenv("AGENTBELL_DEBUG") == "1" {
		fmt.Fprintln(os.Stderr, "agentbell runtime proof:", proofErr)
	}
	if !shouldNotify {
		if os.Getenv("AGENTBELL_DEBUG") == "1" {
			return writeJSON(stdout, map[string]any{
				"suppressed": true,
				"event":      notification.Event,
				"reason":     "effective approval reviewer is not explicitly user",
			})
		}
		return nil
	}
	queueValue, err := queue.Open(filepath.Join(resolved.StateDir, "queue"))
	if err != nil {
		return failOpenError(*failOpen, err)
	}
	id, duplicate, err := queueValue.Enqueue(notification, now)
	if err != nil {
		return failOpenError(*failOpen, err)
	}
	if os.Getenv("AGENTBELL_DEBUG") == "1" {
		return writeJSON(stdout, map[string]any{
			"id": id, "duplicate": duplicate, "event": notification.Event,
		})
	}
	return nil
}

func runService(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: agentbell service <run|install|status|uninstall>")
	}
	if args[0] != "run" {
		return runServiceManager(args, stdout)
	}
	flags := flag.NewFlagSet("service run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	foreground := flags.Bool("foreground", false, "run in foreground")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if !*foreground {
		return errors.New("M0.5 requires service run --foreground")
	}
	resolved, err := paths.Resolve()
	if err != nil {
		return err
	}
	queueValue, err := queue.Open(filepath.Join(resolved.StateDir, "queue"))
	if err != nil {
		return err
	}
	runner := &service.Service{
		Queue: queueValue,
		LoadConfig: func() (config.Config, error) {
			return config.Load(resolved.ConfigFile)
		},
		SenderFactory: func(settings config.Config) service.Sender {
			return transport.LarkCLI{Command: settings.LarkCLIPath}
		},
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runner.Run(ctx)
}

func runServiceManager(args []string, stdout io.Writer) error {
	if args[0] != "install" && args[0] != "status" && args[0] != "uninstall" {
		return fmt.Errorf("unsupported service command %q", args[0])
	}
	flags := flag.NewFlagSet("service "+args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dryRun := flags.Bool("dry-run", false, "show changes without applying them")
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if args[0] == "status" && *dryRun {
		return errors.New("service status does not support --dry-run")
	}
	resolved, err := paths.Resolve()
	if err != nil {
		return err
	}
	var loadedConfig config.Config
	configLoaded := false
	larkCLIPath := ""
	if loaded, loadErr := config.Load(resolved.ConfigFile); loadErr == nil {
		loadedConfig = loaded
		configLoaded = true
		larkCLIPath = loaded.LarkCLIPath
	} else if !errors.Is(loadErr, config.ErrNotFound) {
		return loadErr
	}
	if args[0] == "install" {
		if !configLoaded {
			return errors.New(`agentbell config not found; run "agentbell setup" first`)
		}
		if larkCLIPath != "" {
			if _, lookErr := exec.LookPath(larkCLIPath); lookErr != nil {
				larkCLIPath = ""
			}
		}
		if larkCLIPath == "" {
			resolvedLark, lookErr := exec.LookPath("lark-cli")
			if lookErr != nil {
				return errors.New("lark-cli is unavailable; run AgentBell setup from an environment where lark-cli works")
			}
			larkCLIPath, err = filepath.Abs(resolvedLark)
			if err != nil {
				return err
			}
		}
		if !*dryRun && loadedConfig.LarkCLIPath != larkCLIPath {
			loadedConfig.LarkCLIPath = larkCLIPath
			if err := config.Save(resolved.ConfigFile, &loadedConfig); err != nil {
				return fmt.Errorf("persist lark-cli path: %w", err)
			}
		}
	}
	manager, err := newServiceManager(larkCLIPath, resolved.LogDir)
	if err != nil {
		return err
	}
	var result service.ManagerResult
	switch args[0] {
	case "install":
		result, err = manager.Install(context.Background(), *dryRun)
	case "status":
		result, err = manager.Status(context.Background())
	case "uninstall":
		result, err = manager.Uninstall(context.Background(), *dryRun)
	}
	if err != nil {
		return err
	}
	if *asJSON {
		return writeJSON(stdout, result)
	}
	fmt.Fprintln(stdout, result.Message)
	if result.DefinitionPath != "" {
		fmt.Fprintln(stdout, result.DefinitionPath)
	}
	return nil
}

func runDoctor(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	resolved, err := paths.Resolve()
	if err != nil {
		return err
	}
	queueValue, err := queue.Open(filepath.Join(resolved.StateDir, "queue"))
	if err != nil {
		return err
	}
	stats, err := queueValue.Stats()
	if err != nil {
		return err
	}
	loadedConfig, configErr := config.Load(resolved.ConfigFile)
	var larkErr error
	larkPath := ""
	if configErr == nil && loadedConfig.LarkCLIPath != "" {
		larkPath = loadedConfig.LarkCLIPath
		_, larkErr = os.Stat(larkPath)
	} else {
		larkPath, larkErr = exec.LookPath("lark-cli")
	}
	result := map[string]any{
		"version":      version.Current(),
		"paths":        resolved,
		"queue":        stats,
		"config":       statusForError(configErr),
		"larkCli":      statusForError(larkErr),
		"larkCliPath":  larkPath,
		"platform":     runtime.GOOS,
		"architecture": runtime.GOARCH,
	}
	if *asJSON {
		return writeJSON(stdout, result)
	}
	fmt.Fprintf(
		stdout,
		"AgentBell %s\nConfig: %s\nlark-cli: %s\nQueue: %d pending, %d inflight, %d dead\n",
		version.Current().Version,
		result["config"],
		result["larkCli"],
		stats.Pending,
		stats.Inflight,
		stats.Dead,
	)
	return nil
}

func runQueue(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: agentbell queue <list|retry>")
	}
	resolved, err := paths.Resolve()
	if err != nil {
		return err
	}
	queueValue, err := queue.Open(filepath.Join(resolved.StateDir, "queue"))
	if err != nil {
		return err
	}
	switch args[0] {
	case "list":
		flags := flag.NewFlagSet("queue list", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		stateValue := flags.String("state", "pending", "queue state")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		state := queue.State(*stateValue)
		if state == "history" {
			state = queue.StateSucceeded
		}
		items, err := queueValue.List(state)
		if err != nil {
			return err
		}
		return writeJSON(stdout, items)
	case "retry":
		if len(args) != 2 {
			return errors.New("usage: agentbell queue retry <event-id>")
		}
		if err := queueValue.Retry(args[1], time.Now()); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "queued")
		return nil
	default:
		return fmt.Errorf("unsupported queue command %q", args[0])
	}
}

// cliAdapter is the slice of an adapter the `agentbell adapter` command needs.
type cliAdapter interface {
	Plan() adapter.AdapterPlan
	Install(dryRun bool) (adapter.AdapterResult, error)
	Verify() (adapter.AdapterResult, error)
	Uninstall(dryRun bool) (adapter.AdapterResult, error)
	Diagnose() adapter.AdapterResult
}

func adapterForID(id, stateDir string) (cliAdapter, error) {
	switch id {
	case "codex":
		return adapter.NewCodexAdapter("", stateDir)
	case "claude-code":
		return adapter.NewClaudeAdapter("", stateDir)
	case "kimi-code":
		return adapter.NewKimiAdapter("", stateDir)
	default:
		return nil, fmt.Errorf("adapter %q is not implemented", id)
	}
}

var supportedAdapterIDs = []string{"codex", "claude-code", "kimi-code"}

func supportedAdapters(stateDir string) ([]cliAdapter, error) {
	result := make([]cliAdapter, 0, len(supportedAdapterIDs))
	for _, id := range supportedAdapterIDs {
		value, err := adapterForID(id, stateDir)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}

func uninstallAdapters(
	selected []cliAdapter,
	dryRun bool,
) ([]adapter.AdapterResult, error) {
	results := make([]adapter.AdapterResult, 0, len(selected))
	for index, value := range selected {
		result, err := value.Uninstall(dryRun)
		if err != nil {
			return nil, fmt.Errorf("uninstall %s: %w", supportedAdapterIDs[index], err)
		}
		results = append(results, result)
	}
	return results, nil
}

func runAdapter(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: agentbell adapter <detect|plan|install|verify|uninstall|diagnose>")
	}
	resolved, err := paths.Resolve()
	if err != nil {
		return err
	}

	switch args[0] {
	case "detect":
		id := "codex"
		if len(args) > 1 && args[1] != "--json" {
			id = args[1]
		}
		selected, err := adapterForID(id, resolved.StateDir)
		if err != nil {
			return err
		}
		return writeJSON(stdout, selected.Diagnose())
	case "plan":
		if len(args) < 2 {
			return errors.New("usage: agentbell adapter plan <codex|claude-code|kimi-code>")
		}
		selected, err := adapterForID(args[1], resolved.StateDir)
		if err != nil {
			return err
		}
		return writeJSON(stdout, selected.Plan())
	case "install", "uninstall":
		if len(args) < 2 {
			return fmt.Errorf(
				"usage: agentbell adapter %s <codex|claude-code|kimi-code|all> [--dry-run]",
				args[0],
			)
		}
		dryRun := false
		for _, argument := range args[2:] {
			if argument == "--dry-run" {
				dryRun = true
			}
		}
		if args[0] == "uninstall" && args[1] == "all" {
			selected, err := supportedAdapters(resolved.StateDir)
			if err != nil {
				return err
			}
			if !dryRun {
				if _, err := uninstallAdapters(selected, true); err != nil {
					return fmt.Errorf("preflight %w", err)
				}
			}
			results, err := uninstallAdapters(selected, dryRun)
			if err != nil {
				return err
			}
			return writeJSON(stdout, results)
		}
		selected, err := adapterForID(args[1], resolved.StateDir)
		if err != nil {
			return err
		}
		var result adapter.AdapterResult
		if args[0] == "install" {
			result, err = selected.Install(dryRun)
		} else {
			result, err = selected.Uninstall(dryRun)
		}
		if err != nil {
			return err
		}
		return writeJSON(stdout, result)
	case "verify":
		if len(args) < 2 {
			return errors.New("usage: agentbell adapter verify <codex|claude-code|kimi-code>")
		}
		selected, err := adapterForID(args[1], resolved.StateDir)
		if err != nil {
			return err
		}
		result, err := selected.Verify()
		if err != nil {
			return err
		}
		return writeJSON(stdout, result)
	case "diagnose":
		if len(args) < 2 {
			return errors.New("usage: agentbell adapter diagnose <codex|claude-code|kimi-code>")
		}
		selected, err := adapterForID(args[1], resolved.StateDir)
		if err != nil {
			return err
		}
		return writeJSON(stdout, selected.Diagnose())
	default:
		return fmt.Errorf("unsupported adapter command %q", args[0])
	}
}

type productUninstallReport struct {
	DryRun      bool                    `json:"dryRun"`
	Service     service.ManagerResult   `json:"service"`
	Adapters    []adapter.AdapterResult `json:"adapters"`
	CoreCleanup string                  `json:"coreCleanup"`
	Preserved   []string                `json:"preserved"`
}

func runProductUninstall(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dryRun := flags.Bool("dry-run", false, "show changes without applying them")
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: agentbell uninstall [--dry-run] [--json]")
	}
	resolved, err := paths.Resolve()
	if err != nil {
		return err
	}
	manager, err := newServiceManager("", resolved.LogDir)
	if err != nil {
		return err
	}
	selected, err := supportedAdapters(resolved.StateDir)
	if err != nil {
		return err
	}

	serviceResult, err := manager.Uninstall(context.Background(), true)
	if err != nil {
		return fmt.Errorf("preflight service uninstall: %w", err)
	}
	adapterResults, err := uninstallAdapters(selected, true)
	if err != nil {
		return fmt.Errorf("preflight %w", err)
	}
	if !*dryRun {
		serviceResult, err = manager.Uninstall(context.Background(), false)
		if err != nil {
			return fmt.Errorf("uninstall service: %w", err)
		}
		adapterResults, err = uninstallAdapters(selected, false)
		if err != nil {
			return err
		}
	}
	report := productUninstallReport{
		DryRun:      *dryRun,
		Service:     serviceResult,
		Adapters:    adapterResults,
		CoreCleanup: "npm bootstrap removes its managed Core version after this process exits; direct binary invocations retain the executable",
		Preserved:   []string{resolved.ConfigFile, resolved.StateDir},
	}
	if *asJSON {
		return writeJSON(stdout, report)
	}
	if *dryRun {
		fmt.Fprintln(stdout, "AgentBell product uninstall plan:")
	} else {
		fmt.Fprintln(stdout, "AgentBell login service and supported product hooks are uninstalled.")
	}
	fmt.Fprintf(stdout, "Service: %s\n", serviceResult.Message)
	for _, result := range adapterResults {
		fmt.Fprintf(stdout, "%s: %s\n", result.Adapter, result.Message)
	}
	fmt.Fprintln(stdout, report.CoreCleanup)
	fmt.Fprintln(stdout, "Configuration and queue data were preserved:")
	for _, path := range report.Preserved {
		fmt.Fprintf(stdout, "  %s\n", path)
	}
	return nil
}

func runSetup(args []string, stdin io.Reader, stdout io.Writer) error {
	flags := flag.NewFlagSet("setup", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dryRun := flags.Bool("dry-run", false, "print the plan without changes")
	asJSON := flags.Bool("json", false, "print JSON report")
	if err := flags.Parse(args); err != nil {
		return err
	}
	resolved, err := paths.Resolve()
	if err != nil {
		return err
	}
	flow := &setup.Setup{
		Prompter:   setup.NewStdioPrompter(stdin, stdout),
		ConfigFile: resolved.ConfigFile,
		StateDir:   resolved.StateDir,
		DryRun:     *dryRun,
		Out:        stdout,
		InstallService: func(ctx context.Context) (string, error) {
			loaded, err := config.Load(resolved.ConfigFile)
			if err != nil {
				return "", err
			}
			manager, err := newServiceManager(loaded.LarkCLIPath, resolved.LogDir)
			if err != nil {
				return "", err
			}
			result, err := manager.Install(ctx, false)
			if result.DefinitionPath != "" {
				return result.DefinitionPath, err
			}
			return result.Label, err
		},
	}
	report, err := flow.Run(context.Background())
	if *asJSON {
		if jsonErr := writeJSON(stdout, report); jsonErr != nil {
			return jsonErr
		}
	}
	return err
}

func runTest(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	channelID := flags.String("channel", "", "channel id")
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	resolved, err := paths.Resolve()
	if err != nil {
		return err
	}
	loaded, err := config.Load(resolved.ConfigFile)
	if errors.Is(err, config.ErrNotFound) {
		return errors.New(`agentbell config not found; run "agentbell setup" first`)
	}
	if err != nil {
		return err
	}
	var channel config.Channel
	found := false
	if *channelID != "" {
		for _, candidate := range loaded.Channels {
			if candidate.ID == *channelID {
				channel = candidate
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("channel %q not found in config", *channelID)
		}
	} else {
		channel, found = loaded.Default()
		if !found {
			return fmt.Errorf("default channel %q not found in config", loaded.DefaultChannel)
		}
	}

	now := time.Now()
	text := fmt.Sprintf(
		"🔔 AgentBell 测试消息\n时间：%s\n如果你看到这条消息，说明通知链路已配置成功。",
		now.Format("2006-01-02 15:04:05"),
	)
	sender := transport.LarkCLI{Command: loaded.LarkCLIPath}
	sendErr := sender.Send(context.Background(), channel, text)
	if *asJSON {
		result := map[string]any{
			"ok":      sendErr == nil,
			"channel": channel.ID,
			"chatId":  channel.ChatID,
			"sentAt":  now.UTC().Format(time.RFC3339),
		}
		if sendErr != nil {
			result["error"] = sendErr.Error()
		}
		if err := writeJSON(stdout, result); err != nil {
			return err
		}
	} else if sendErr == nil {
		fmt.Fprintf(stdout, "测试消息已发送到频道 %s（%s）\n", channel.ID, channel.ChatID)
	}
	return sendErr
}

func readLimited(reader io.Reader, maximum int64) ([]byte, error) {
	limited := io.LimitReader(reader, maximum+1)
	value, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(value)) > maximum {
		return nil, fmt.Errorf("hook input exceeds %d bytes", maximum)
	}
	if len(strings.TrimSpace(string(value))) == 0 {
		return nil, errors.New("hook input is empty")
	}
	return value, nil
}

func failOpenError(failOpen bool, err error) error {
	if failOpen {
		if os.Getenv("AGENTBELL_DEBUG") == "1" {
			fmt.Fprintln(os.Stderr, "agentbell fail-open:", err)
		}
		return nil
	}
	return err
}

func statusForError(err error) string {
	if err == nil {
		return "ok"
	}
	if errors.Is(err, config.ErrNotFound) || errors.Is(err, exec.ErrNotFound) {
		return "missing"
	}
	return "error: " + err.Error()
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
