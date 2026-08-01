// Package bridge implements the version-independent AgentBell process invoked
// by product hooks and login-service definitions.
package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/liming0791/agentbell/core/internal/installstate"
)

const (
	ProtocolVersion = 1
	MaxHookInput    = 256 * 1024
)

var allowedSurfaces = map[string]map[string]bool{
	"codex": {
		"cli":     true,
		"desktop": true,
	},
	"claude-code": {
		"cli":     true,
		"desktop": true,
	},
	"kimi-code": {
		"cli": true,
	},
}

type Runner interface {
	Run(
		context.Context,
		string,
		[]string,
		[]byte,
		io.Writer,
		io.Writer,
	) error
}

type ExecRunner struct{}

func (ExecRunner) Run(
	ctx context.Context,
	executable string,
	args []string,
	input []byte,
	stdout io.Writer,
	stderr io.Writer,
) error {
	command := exec.CommandContext(ctx, executable, args...)
	command.Stdin = bytes.NewReader(input)
	command.Stdout = stdout
	command.Stderr = stderr
	configureBackgroundChild(command)
	return command.Run()
}

type App struct {
	Store          installstate.Store
	Runner         Runner
	ExecutablePath func() (string, error)
	Target         string
}

func New() *App {
	target, _ := CurrentTarget(runtime.GOOS, runtime.GOARCH)
	return &App{
		Store:          installstate.NewStore(installstate.OSFileSystem{}),
		Runner:         ExecRunner{},
		ExecutablePath: os.Executable,
		Target:         target,
	}
}

func (app *App) Run(
	ctx context.Context,
	args []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
) int {
	if len(args) == 0 {
		return 2
	}
	switch args[0] {
	case "hook-v1":
		// Product hooks must remain fail-open even when their arguments, active
		// state, Core binary, or child execution are broken.
		_ = app.runHook(ctx, args, stdin)
		return 0
	case "service-v1":
		if len(args) != 1 {
			return 2
		}
		if err := app.runService(ctx, stdout, stderr); err != nil {
			fmt.Fprintln(stderr, "agentbell bridge service:", err)
			return 1
		}
		return 0
	default:
		return 2
	}
}

type hookRequest struct {
	Adapter string
	Surface string
}

func (app *App) runHook(ctx context.Context, args []string, stdin io.Reader) error {
	request, err := parseHookRequest(args)
	if err != nil {
		return err
	}
	input, err := readOneJSON(stdin, MaxHookInput)
	if err != nil {
		return err
	}
	corePath, state, _, err := app.activeCore()
	if err != nil {
		return err
	}
	coreArgs := []string{
		"emit",
		"--adapter", request.Adapter,
		"--surface", request.Surface,
		"--runtime", "host",
		"--stdin",
		"--fail-open",
	}
	// Cores before M2 do not recognize the generation-proof flags. The stable
	// bridge must keep their Hook path usable after an explicit rollback.
	if supportsGenerationProof(state.ActiveVersion) {
		coreArgs = append(
			coreArgs,
			"--bridge-protocol", strconv.Itoa(ProtocolVersion),
			"--activation-generation", strconv.FormatUint(state.Generation, 10),
		)
	}
	return app.runner().Run(ctx, corePath, coreArgs, input, io.Discard, io.Discard)
}

func supportsGenerationProof(version string) bool {
	base := strings.SplitN(version, "-", 2)[0]
	parts := strings.Split(base, ".")
	if len(parts) != 3 {
		return false
	}
	major, majorErr := strconv.Atoi(parts[0])
	minor, minorErr := strconv.Atoi(parts[1])
	if majorErr != nil || minorErr != nil {
		return false
	}
	return major > 0 || minor >= 3
}

func (app *App) runService(
	ctx context.Context,
	stdout io.Writer,
	stderr io.Writer,
) error {
	corePath, state, dataRoot, err := app.activeCore()
	if err != nil {
		return err
	}
	if state.ServiceVersion != "" {
		serviceState := state
		serviceState.ActiveVersion = state.ServiceVersion
		serviceState.PreviousVersion = ""
		serviceState.Checksum = state.ServiceChecksum
		serviceState.ServiceVersion = ""
		serviceState.ServiceChecksum = ""
		corePath, err = app.Store.ResolveManagedCore(dataRoot, serviceState)
		if err != nil {
			return fmt.Errorf("resolve service Core: %w", err)
		}
	}
	return app.runner().Run(
		ctx,
		corePath,
		[]string{"service", "run", "--foreground"},
		nil,
		stdout,
		stderr,
	)
}

func (app *App) activeCore() (
	string,
	installstate.ActiveState,
	string,
	error,
) {
	executablePath := app.ExecutablePath
	if executablePath == nil {
		executablePath = os.Executable
	}
	executable, err := executablePath()
	if err != nil {
		return "", installstate.ActiveState{}, "", err
	}
	dataRoot, err := installstate.DataRootFromBridgePath(executable)
	if err != nil {
		return "", installstate.ActiveState{}, "", err
	}
	state, err := app.Store.Load(dataRoot)
	if err != nil {
		return "", installstate.ActiveState{}, "", err
	}
	target := app.Target
	if target == "" {
		target, err = CurrentTarget(runtime.GOOS, runtime.GOARCH)
		if err != nil {
			return "", installstate.ActiveState{}, "", err
		}
	}
	if state.Target != target {
		return "", installstate.ActiveState{}, "", fmt.Errorf(
			"active Core target %s does not match bridge target %s",
			state.Target,
			target,
		)
	}
	corePath, err := app.Store.ResolveManagedCore(dataRoot, state)
	if err != nil {
		return "", installstate.ActiveState{}, "", err
	}
	return corePath, state, dataRoot, nil
}

func (app *App) runner() Runner {
	if app.Runner != nil {
		return app.Runner
	}
	return ExecRunner{}
}

func parseHookRequest(args []string) (hookRequest, error) {
	if len(args) != 9 ||
		args[0] != "hook-v1" ||
		args[1] != "--adapter" ||
		args[3] != "--surface" ||
		args[5] != "--runtime" ||
		args[6] != "host" ||
		args[7] != "--stdin" ||
		args[8] != "--fail-open" {
		return hookRequest{}, errors.New("invalid hook-v1 arguments")
	}
	request := hookRequest{Adapter: args[2], Surface: args[4]}
	surfaces, ok := allowedSurfaces[request.Adapter]
	if !ok || !surfaces[request.Surface] {
		return hookRequest{}, errors.New("unsupported hook-v1 adapter or surface")
	}
	return request, nil
}

func readOneJSON(reader io.Reader, maximum int64) ([]byte, error) {
	limited := &io.LimitedReader{R: reader, N: maximum + 1}
	var value json.RawMessage
	if err := json.NewDecoder(limited).Decode(&value); err != nil {
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

func CurrentTarget(goos, goarch string) (string, error) {
	switch goos + "/" + goarch {
	case "windows/amd64":
		return "windows-amd64", nil
	case "windows/arm64":
		return "windows-arm64", nil
	case "darwin/amd64":
		return "darwin-amd64", nil
	case "darwin/arm64":
		return "darwin-arm64", nil
	case "linux/amd64":
		return "linux-amd64", nil
	case "linux/arm64":
		return "linux-arm64", nil
	default:
		return "", fmt.Errorf("unsupported bridge target %s/%s", goos, goarch)
	}
}
