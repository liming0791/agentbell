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
	"github.com/liming0791/agentbell/core/internal/transport"
	"github.com/liming0791/agentbell/core/internal/version"
)

const maxInputSize = 256 * 1024

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
		err = runService(args[1:])
	case "doctor":
		err = runDoctor(args[1:], stdout)
	case "queue":
		err = runQueue(args[1:], stdout)
	case "adapter":
		err = runAdapter(args[1:], stdout)
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
  agentbell service run --foreground
  agentbell doctor [--json]
  agentbell queue list [--state pending|inflight|dead]
  agentbell queue retry <event-id>
  agentbell adapter <detect|plan|install|verify|uninstall> ...`)
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
	notification, err := event.Normalize(*adapterID, *surface, *runtimeName, raw, time.Now())
	if err != nil {
		return failOpenError(*failOpen, err)
	}
	resolved, err := paths.Resolve()
	if err != nil {
		return failOpenError(*failOpen, err)
	}
	queueValue, err := queue.Open(filepath.Join(resolved.StateDir, "queue"))
	if err != nil {
		return failOpenError(*failOpen, err)
	}
	id, duplicate, err := queueValue.Enqueue(notification, time.Now())
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

func runService(args []string) error {
	if len(args) == 0 || args[0] != "run" {
		return errors.New("usage: agentbell service run --foreground")
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
		Sender: transport.LarkCLI{},
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runner.Run(ctx)
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
	_, configErr := config.Load(resolved.ConfigFile)
	_, larkErr := exec.LookPath("lark-cli")
	result := map[string]any{
		"version":      version.Current(),
		"paths":        resolved,
		"queue":        stats,
		"config":       statusForError(configErr),
		"larkCli":      statusForError(larkErr),
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

func runAdapter(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: agentbell adapter <detect|plan|install|verify|uninstall|diagnose>")
	}
	resolved, err := paths.Resolve()
	if err != nil {
		return err
	}
	codexAdapter, err := adapter.NewCodexAdapter("", resolved.StateDir)
	if err != nil {
		return err
	}

	switch args[0] {
	case "detect":
		id := "codex"
		if len(args) > 1 && args[1] != "--json" {
			id = args[1]
		}
		if id != "codex" {
			return fmt.Errorf("adapter %q is not implemented in M0.5", id)
		}
		return writeJSON(stdout, codexAdapter.Diagnose())
	case "plan":
		if len(args) < 2 || args[1] != "codex" {
			return errors.New("usage: agentbell adapter plan codex")
		}
		return writeJSON(stdout, codexAdapter.Plan())
	case "install", "uninstall":
		if len(args) < 2 || args[1] != "codex" {
			return fmt.Errorf("usage: agentbell adapter %s codex [--dry-run]", args[0])
		}
		dryRun := false
		for _, argument := range args[2:] {
			if argument == "--dry-run" {
				dryRun = true
			}
		}
		var result adapter.AdapterResult
		if args[0] == "install" {
			result, err = codexAdapter.Install(dryRun)
		} else {
			result, err = codexAdapter.Uninstall(dryRun)
		}
		if err != nil {
			return err
		}
		return writeJSON(stdout, result)
	case "verify":
		if len(args) < 2 || args[1] != "codex" {
			return errors.New("usage: agentbell adapter verify codex")
		}
		result, err := codexAdapter.Verify()
		if err != nil {
			return err
		}
		return writeJSON(stdout, result)
	case "diagnose":
		if len(args) < 2 || args[1] != "codex" {
			return errors.New("usage: agentbell adapter diagnose codex")
		}
		return writeJSON(stdout, codexAdapter.Diagnose())
	default:
		return fmt.Errorf("unsupported adapter command %q", args[0])
	}
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
