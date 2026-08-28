package app

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/liming0791/agentbell/core/internal/adapter"
	"github.com/liming0791/agentbell/core/internal/binding"
	"github.com/liming0791/agentbell/core/internal/bridge"
	"github.com/liming0791/agentbell/core/internal/config"
	"github.com/liming0791/agentbell/core/internal/event"
	"github.com/liming0791/agentbell/core/internal/installstate"
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
	case "settings":
		err = runSettings(args[1:], stdout)
	case "policy":
		err = runPolicy(args[1:], stdout)
	case "bind":
		err = runBind(args[1:], stdin, stdout)
	case "hook":
		err = runHook(args[1:], stdout)
	case "bridge":
		err = runBridge(args[1:], stdout)
	case "plugin":
		err = runPlugin(args[1:], stdout)
	case "relay":
		err = runRelay(args[1:], stdin, stdout)
	case "remote":
		err = runRemote(args[1:], stdin, stdout)
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
  agentbell service <run --foreground|install|status|restart|uninstall>
  agentbell doctor [--json]
  agentbell queue list [--state pending|inflight|dead]
  agentbell queue retry <event-id>
  agentbell settings <show|channel|event|template|quiet-hours> ...
  agentbell policy <status|explain> ...
  agentbell bind <create|complete|status|cancel> ...
  agentbell hook <conflicts|reconcile> [all|codex|claude-code|kimi-code] [--dry-run] [--json]
  agentbell bridge doctor [--json]
  agentbell plugin verify <bundle> [--json]
  agentbell relay <configure|run|bind create|peers list|peers revoke|receipts list|connector add|connector list|connector remove|connector pair> ...
  agentbell remote emit --adapter <id> --surface <surface> --runtime <runtime> --stdin [--fail-open]
  agentbell remote configure --runtime <runtime> --connector <type> ...
  agentbell remote pair --code-stdin [--endpoint <url>] [--ssh-tunnel]
  agentbell remote test --adapter <id> --surface <surface> [--json]
  agentbell remote drain --stdio
  agentbell adapter <detect|plan|install|verify|uninstall|diagnose> <codex|claude-code|kimi-code|opencode|qoder|qoder-work|trae>
  agentbell adapter uninstall all [--dry-run]
  agentbell uninstall [--dry-run] [--json] [--delete-remote-credential --confirm-delete-remote-credential]
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
	bridgeProtocol := flags.Int(
		"bridge-protocol",
		0,
		"internal stable bridge protocol",
	)
	activationGeneration := flags.Uint64(
		"activation-generation",
		0,
		"internal active Core generation",
	)
	if err := flags.Parse(args); err != nil {
		return failOpenError(*failOpen, err)
	}
	if *adapterID == "" || *surface == "" || *runtimeName == "" || !*useStdin {
		return failOpenError(*failOpen, errors.New(
			"emit requires --adapter, --surface, --runtime and --stdin",
		))
	}
	proofContext := adapter.RuntimeProofContext{}
	if *bridgeProtocol != 0 || *activationGeneration != 0 {
		if *bridgeProtocol != 1 {
			return failOpenError(
				*failOpen,
				errors.New("bridge protocol must be 1"),
			)
		}
		if *activationGeneration == 0 {
			return failOpenError(
				*failOpen,
				errors.New("activation generation is required"),
			)
		}
		proofContext = adapter.RuntimeProofContext{
			BridgeProtocol:       *bridgeProtocol,
			CoreVersion:          version.Current().Version,
			ActivationGeneration: *activationGeneration,
		}
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
	if proofErr := adapter.RecordRuntimeProofWithContext(
		resolved.StateDir,
		*adapterID,
		notification.Event,
		now,
		proofContext,
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
		return errors.New("usage: agentbell service <run|install|status|restart|uninstall>")
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
		Processor: newM2Processor(resolved),
	}
	if remoteWorkers, workerErr := configuredRemoteWorkers(resolved); workerErr == nil {
		runner.Workers = append(runner.Workers, remoteWorkers...)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runner.Run(ctx)
}

func runServiceManager(args []string, stdout io.Writer) error {
	if args[0] != "install" &&
		args[0] != "status" &&
		args[0] != "restart" &&
		args[0] != "uninstall" {
		return fmt.Errorf("unsupported service command %q", args[0])
	}
	flags := flag.NewFlagSet("service "+args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dryRun := flags.Bool("dry-run", false, "show changes without applying them")
	asJSON := flags.Bool("json", false, "print JSON")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if (args[0] == "status" || args[0] == "restart") && *dryRun {
		return fmt.Errorf("service %s does not support --dry-run", args[0])
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
	manager, err := configuredServiceManager(larkCLIPath, resolved)
	if err != nil {
		return err
	}
	var result service.ManagerResult
	switch args[0] {
	case "install":
		result, err = manager.Install(context.Background(), *dryRun)
	case "status":
		result, err = manager.Status(context.Background())
	case "restart":
		result, err = manager.Restart(context.Background())
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
	return runUnifiedDoctor(args, stdout)
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

type adapterRuntime struct {
	CoreExecutable          string
	BridgeExecutable        string
	ServiceBridgeExecutable string
	ActiveGeneration        uint64
}

func adapterForID(id string, resolved paths.Paths) (cliAdapter, error) {
	adapterRuntime, err := resolveAdapterRuntime(resolved)
	if err != nil {
		return nil, err
	}
	return adapterForIDWithRuntime(id, resolved.StateDir, adapterRuntime)
}

func adapterForIDWithRuntime(
	id,
	stateDir string,
	selectedRuntime adapterRuntime,
) (cliAdapter, error) {
	var selected cliAdapter
	var err error
	switch id {
	case "codex":
		var value *adapter.CodexAdapter
		value, err = adapter.NewCodexAdapter(selectedRuntime.CoreExecutable, stateDir)
		if err == nil {
			value.BridgeExecutable = selectedRuntime.BridgeExecutable
			value.ActiveGeneration = selectedRuntime.ActiveGeneration
			selected = value
		}
	case "claude-code":
		var value *adapter.ClaudeAdapter
		value, err = adapter.NewClaudeAdapter(selectedRuntime.CoreExecutable, stateDir)
		if err == nil {
			value.BridgeExecutable = selectedRuntime.BridgeExecutable
			value.ActiveGeneration = selectedRuntime.ActiveGeneration
			selected = value
		}
	case "kimi-code":
		var value *adapter.KimiAdapter
		value, err = adapter.NewKimiAdapter(selectedRuntime.CoreExecutable, stateDir)
		if err == nil {
			value.BridgeExecutable = selectedRuntime.BridgeExecutable
			value.ActiveGeneration = selectedRuntime.ActiveGeneration
			selected = value
		}
	case "opencode":
		selected, err = adapter.NewOpenCodeAdapter(selectedRuntime.CoreExecutable, stateDir)
	case "qoder":
		selected, err = adapter.NewQoderAdapter(selectedRuntime.CoreExecutable, stateDir)
	case "qoder-work":
		selected, err = adapter.NewQoderWorkAdapter(selectedRuntime.CoreExecutable, stateDir)
	case "trae":
		selected, err = adapter.NewTraeAdapter(selectedRuntime.CoreExecutable, stateDir)
	default:
		return nil, fmt.Errorf("adapter %q is not implemented", id)
	}
	return selected, err
}

var supportedAdapterIDs = []string{
	"codex", "claude-code", "kimi-code", "opencode", "qoder", "qoder-work", "trae",
}

func supportedAdapters(resolved paths.Paths) ([]cliAdapter, error) {
	selectedRuntime, err := resolveAdapterRuntime(resolved)
	if err != nil {
		return nil, err
	}
	return supportedAdaptersWithRuntime(
		resolved.StateDir,
		selectedRuntime,
		runtime.GOOS,
	)
}

func supportedAdaptersWithRuntime(
	stateDir string,
	selectedRuntime adapterRuntime,
	goos string,
) ([]cliAdapter, error) {
	result := make([]cliAdapter, 0, len(supportedAdapterIDs))
	for _, id := range supportedAdapterIDs {
		if !adapterSupportedOnPlatform(id, goos) {
			result = append(result, platformSkippedAdapter{
				id:       id,
				platform: goos,
			})
			continue
		}
		value, err := adapterForIDWithRuntime(
			id,
			stateDir,
			selectedRuntime,
		)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}

func adapterSupportedOnPlatform(id, goos string) bool {
	switch id {
	case "qoder-work", "trae":
		return goos == "darwin" || goos == "windows"
	default:
		return true
	}
}

type platformSkippedAdapter struct {
	id       string
	platform string
}

func (value platformSkippedAdapter) Plan() adapter.AdapterPlan {
	return adapter.AdapterPlan{
		Adapter: value.id,
		Changes: []string{value.message()},
	}
}

func (value platformSkippedAdapter) Install(
	bool,
) (adapter.AdapterResult, error) {
	return value.result(), errors.New(value.unsupportedMessage())
}

func (value platformSkippedAdapter) Verify() (adapter.AdapterResult, error) {
	return value.result(), errors.New(value.unsupportedMessage())
}

func (value platformSkippedAdapter) Uninstall(
	bool,
) (adapter.AdapterResult, error) {
	return value.result(), nil
}

func (value platformSkippedAdapter) Diagnose() adapter.AdapterResult {
	return value.result()
}

func (value platformSkippedAdapter) result() adapter.AdapterResult {
	return adapter.AdapterResult{
		Adapter: value.id,
		Message: value.message(),
	}
}

func (value platformSkippedAdapter) message() string {
	return value.unsupportedMessage() + "; skipped"
}

func (value platformSkippedAdapter) unsupportedMessage() string {
	return fmt.Sprintf(
		"%s adapter is not supported on %s",
		value.id,
		value.platform,
	)
}

func resolveAdapterRuntime(resolved paths.Paths) (adapterRuntime, error) {
	executable, err := os.Executable()
	if err != nil {
		return adapterRuntime{}, err
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return adapterRuntime{}, err
	}
	legacy := adapterRuntime{CoreExecutable: executable}

	store := installstate.NewStore(installstate.OSFileSystem{})
	active, err := store.Load(resolved.DataDir)
	if errors.Is(err, fs.ErrNotExist) {
		return legacy, nil
	}
	if err != nil {
		return adapterRuntime{}, fmt.Errorf("load active AgentBell runtime: %w", err)
	}
	target, err := bridge.CurrentTarget(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return adapterRuntime{}, err
	}
	if active.Target != target {
		return adapterRuntime{}, fmt.Errorf(
			"active AgentBell target %s does not match host target %s",
			active.Target,
			target,
		)
	}
	coreExecutable, err := store.ResolveManagedCore(resolved.DataDir, active)
	if err != nil {
		return adapterRuntime{}, fmt.Errorf("resolve active AgentBell Core: %w", err)
	}
	bridgeExecutable, err := stableBridgePath(resolved.DataDir)
	if err != nil {
		return adapterRuntime{}, err
	}
	serviceBridgeExecutable := bridgeExecutable
	if runtime.GOOS == "windows" && active.ServiceBridgeChecksum != "" {
		serviceBridgeExecutable, err = stableBridgeEntryPath(
			resolved.DataDir,
			"agentbell-service.exe",
			"service bridge",
		)
		if err != nil {
			return adapterRuntime{}, err
		}
	}
	return adapterRuntime{
		CoreExecutable:          coreExecutable,
		BridgeExecutable:        bridgeExecutable,
		ServiceBridgeExecutable: serviceBridgeExecutable,
		ActiveGeneration:        active.Generation,
	}, nil
}

func stableBridgePath(dataRoot string) (string, error) {
	if !filepath.IsAbs(dataRoot) {
		return "", errors.New("AgentBell data root must be absolute")
	}
	root := filepath.Clean(dataRoot)
	if root == string(filepath.Separator) {
		return "", errors.New("AgentBell data root cannot be the filesystem root")
	}
	name := "agentbell-bridge"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return stableBridgeEntryPath(root, name, "bridge")
}

func stableBridgeEntryPath(root, name, label string) (string, error) {
	candidate := filepath.Join(root, "bin", "bridge", "v1", name)
	relative, err := filepath.Rel(root, candidate)
	if err != nil ||
		relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) ||
		filepath.IsAbs(relative) {
		return "", errors.New("stable AgentBell bridge path escapes the data root")
	}
	current := root
	components := append(
		[]string{"."},
		strings.Split(relative, string(filepath.Separator))...,
	)
	for _, component := range components {
		if component != "." {
			current = filepath.Join(current, component)
		}
		info, statErr := os.Lstat(current)
		if statErr != nil {
			return "", fmt.Errorf("validate stable AgentBell %s: %w", label, statErr)
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return "", fmt.Errorf(
				"stable AgentBell %s path contains symlink %s",
				label,
				current,
			)
		}
		if current == candidate {
			if !info.Mode().IsRegular() {
				return "", fmt.Errorf("stable AgentBell %s is not a regular file", label)
			}
			if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
				return "", fmt.Errorf("stable AgentBell %s is not executable", label)
			}
		}
	}
	return candidate, nil
}

func configuredServiceManager(
	larkCLIPath string,
	resolved paths.Paths,
) (*service.Manager, error) {
	manager, err := newServiceManager(larkCLIPath, resolved.LogDir)
	if err != nil {
		return nil, err
	}
	manager.StateDir = resolved.StateDir
	selectedRuntime, err := resolveAdapterRuntime(resolved)
	if err != nil {
		return nil, err
	}
	manager.Executable = selectedRuntime.CoreExecutable
	if selectedRuntime.BridgeExecutable == "" {
		manager.ServiceMode = service.ServiceModeLegacy
		manager.BridgeExecutable = ""
		return manager, nil
	}
	manager.ServiceMode = service.ServiceModeBridge
	manager.BridgeExecutable = selectedRuntime.ServiceBridgeExecutable
	return manager, nil
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
		selected, err := adapterForID(id, resolved)
		if err != nil {
			return err
		}
		return writeJSON(stdout, selected.Diagnose())
	case "plan":
		if len(args) < 2 {
			return errors.New("usage: agentbell adapter plan <codex|claude-code|kimi-code|opencode|qoder|qoder-work|trae>")
		}
		selected, err := adapterForID(args[1], resolved)
		if err != nil {
			return err
		}
		return writeJSON(stdout, selected.Plan())
	case "install", "uninstall":
		if len(args) < 2 {
			return fmt.Errorf(
				"usage: agentbell adapter %s <codex|claude-code|kimi-code|opencode|qoder|qoder-work|trae|all> [--dry-run]",
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
			selected, err := supportedAdapters(resolved)
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
		selected, err := adapterForID(args[1], resolved)
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
			return errors.New("usage: agentbell adapter verify <codex|claude-code|kimi-code|opencode|qoder|qoder-work|trae>")
		}
		selected, err := adapterForID(args[1], resolved)
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
			return errors.New("usage: agentbell adapter diagnose <codex|claude-code|kimi-code|opencode|qoder|qoder-work|trae>")
		}
		selected, err := adapterForID(args[1], resolved)
		if err != nil {
			return err
		}
		return writeJSON(stdout, selected.Diagnose())
	default:
		return fmt.Errorf("unsupported adapter command %q", args[0])
	}
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
	selectedRuntime, err := resolveAdapterRuntime(resolved)
	if err != nil {
		return err
	}
	flow := &setup.Setup{
		Prompter:         setup.NewStdioPrompter(stdin, stdout),
		ConfigFile:       resolved.ConfigFile,
		StateDir:         resolved.StateDir,
		CoreExecutable:   selectedRuntime.CoreExecutable,
		BridgeExecutable: selectedRuntime.BridgeExecutable,
		ActiveGeneration: selectedRuntime.ActiveGeneration,
		DryRun:           *dryRun,
		Out:              stdout,
		CreateBinding: func(
			_ context.Context,
			request setup.BindingRequest,
		) (setup.BindingResult, error) {
			store := binding.NewStore(filepath.Join(resolved.StateDir, "bindings"))
			code, record, err := store.Create(
				request.ChannelName,
				request.Identity,
				request.TTL,
				bindingNow().UTC(),
				request.LarkCLIPath,
			)
			if err != nil {
				return setup.BindingResult{}, err
			}
			return setup.BindingResult{
				Code:        code,
				ChannelName: record.ChannelName,
				Identity:    record.As,
				ExpiresAt:   record.ExpiresAt,
			}, nil
		},
		InstallService: func(ctx context.Context) (string, error) {
			loaded, err := config.Load(resolved.ConfigFile)
			if err != nil {
				return "", err
			}
			manager, err := configuredServiceManager(loaded.LarkCLIPath, resolved)
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
	verifyErr := sender.VerifyUserReachability(context.Background(), channel)
	sendErr := verifyErr
	if verifyErr == nil {
		sendErr = sender.Send(context.Background(), channel, text)
	}
	if *asJSON {
		result := map[string]any{
			"ok":                 sendErr == nil,
			"channel":            channel.ID,
			"checkedAt":          now.UTC().Format(time.RFC3339),
			"recipientReachable": verifyErr == nil,
		}
		if sendErr == nil {
			result["sentAt"] = now.UTC().Format(time.RFC3339)
		}
		if sendErr != nil {
			result["error"] = sendErr.Error()
		}
		if err := writeJSON(stdout, result); err != nil {
			return err
		}
	} else if sendErr == nil {
		fmt.Fprintf(stdout, "已验证当前飞书用户可接收通知；测试消息已发送到频道 %s\n", channel.ID)
	}
	return sendErr
}

func readLimited(reader io.Reader, maximum int64) ([]byte, error) {
	// Hook runners do not consistently close the command's stdin before they
	// wait for the command to exit. Decode exactly one JSON value so a complete
	// payload can be accepted without waiting for EOF and deadlocking until the
	// product's Hook timeout kills AgentBell.
	limited := &io.LimitedReader{R: reader, N: maximum + 1}
	var value json.RawMessage
	err := json.NewDecoder(limited).Decode(&value)
	if err != nil {
		if limited.N == 0 {
			return nil, fmt.Errorf("hook input exceeds %d bytes", maximum)
		}
		if errors.Is(err, io.EOF) {
			return nil, errors.New("hook input is empty")
		}
		return nil, err
	}
	if int64(len(value)) > maximum {
		return nil, fmt.Errorf("hook input exceeds %d bytes", maximum)
	}
	if len(value) == 0 {
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

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}
