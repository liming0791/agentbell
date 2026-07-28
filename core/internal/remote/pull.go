package remote

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/liming0791/agentbell/core/internal/relay"
	"github.com/liming0791/agentbell/core/internal/remoteconfig"
)

const (
	defaultPullTimeout = 2 * time.Minute
	defaultMaxFrames   = 4096
)

var (
	ErrInvalidPuller     = errors.New("remote pull connector dependencies are incomplete")
	ErrConnectorStart    = errors.New("remote connector process could not start")
	ErrConnectorProtocol = errors.New("remote connector protocol failed")
	ErrConnectorExit     = errors.New("remote connector process failed")
	ErrFrameLimit        = errors.New("remote connector frame limit reached")
)

type Ingress interface {
	Accept(relay.IngressRequest) (relay.IngressACK, error)
}

type Process interface {
	Stdin() io.WriteCloser
	Stdout() io.ReadCloser
	Wait() error
	Close() error
}

type Runner interface {
	Start(context.Context, CommandSpec) (Process, error)
}

// ExecRunner starts the exact executable and argv without a shell. Child
// stderr is discarded because a remote program can print envelope or key
// material there; callers receive only stable, sanitized errors.
type ExecRunner struct{}

func (ExecRunner) Start(
	ctx context.Context,
	spec CommandSpec,
) (Process, error) {
	if ctx == nil || spec.Executable == "" {
		return nil, ErrConnectorStart
	}
	command := exec.CommandContext(
		ctx,
		spec.Executable,
		append([]string(nil), spec.Arguments...)...,
	)
	command.Stderr = io.Discard
	command.WaitDelay = 2 * time.Second
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, ErrConnectorStart
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, ErrConnectorStart
	}
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, ErrConnectorStart
	}
	return &execProcess{
		command: command,
		stdin:   stdin,
		stdout:  stdout,
	}, nil
}

type execProcess struct {
	command   *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	closeOnce sync.Once
}

func (process *execProcess) Stdin() io.WriteCloser { return process.stdin }
func (process *execProcess) Stdout() io.ReadCloser { return process.stdout }
func (process *execProcess) Wait() error           { return process.command.Wait() }

func (process *execProcess) Close() error {
	var closeError error
	process.closeOnce.Do(func() {
		if err := process.stdin.Close(); err != nil {
			closeError = err
		}
		if err := process.stdout.Close(); err != nil && closeError == nil {
			closeError = err
		}
		if process.command.Process != nil {
			if err := process.command.Process.Kill(); err != nil &&
				!errors.Is(err, os.ErrProcessDone) &&
				closeError == nil {
				closeError = err
			}
		}
	})
	return closeError
}

type Puller struct {
	Runner    Runner
	Ingress   Ingress
	Now       func() time.Time
	Timeout   time.Duration
	MaxFrames int
}

// Pull starts a WSL, SSH or container remote drain command and receives its
// bounded stdio frames. It returns an ACK only after Ingress has committed its
// queue item and receipt.
func (puller Puller) Pull(
	ctx context.Context,
	config remoteconfig.RemoteConfig,
) (int, error) {
	if ctx == nil {
		return 0, context.Canceled
	}
	if puller.Ingress == nil ||
		puller.Timeout < 0 ||
		puller.MaxFrames < 0 {
		return 0, ErrInvalidPuller
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	spec, err := BuildPullCommand(config)
	if err != nil {
		return 0, err
	}
	return puller.pullSpec(ctx, spec)
}

func (puller Puller) PullConnector(
	ctx context.Context,
	target remoteconfig.HostConnector,
) (int, error) {
	if err := target.Validate(); err != nil {
		return 0, ErrInvalidRemoteConfig
	}
	spec, err := BuildPullCommandForConnector(
		target.Runtime,
		target.Connector,
	)
	if err != nil {
		return 0, err
	}
	return puller.pullSpec(ctx, spec)
}

func (puller Puller) pullSpec(
	ctx context.Context,
	spec CommandSpec,
) (int, error) {
	timeout := puller.Timeout
	if timeout == 0 {
		timeout = defaultPullTimeout
	}
	maxFrames := puller.MaxFrames
	if maxFrames == 0 {
		maxFrames = defaultMaxFrames
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	runner := puller.Runner
	if runner == nil {
		runner = ExecRunner{}
	}
	process, err := runner.Start(runCtx, spec)
	if err != nil || process == nil ||
		process.Stdin() == nil ||
		process.Stdout() == nil {
		if process != nil {
			_ = process.Close()
		}
		return 0, ErrConnectorStart
	}
	defer process.Close()

	for count := 0; ; count++ {
		if count >= maxFrames {
			_ = process.Close()
			_ = waitProcess(runCtx, process)
			return count, ErrFrameLimit
		}
		request, err := readForwardRequest(runCtx, process)
		if errors.Is(err, io.EOF) {
			if waitErr := waitProcess(runCtx, process); waitErr != nil {
				if contextError(runCtx) != nil {
					return count, contextError(runCtx)
				}
				return count, ErrConnectorExit
			}
			return count, nil
		}
		if err != nil {
			if contextError(runCtx) != nil {
				return count, contextError(runCtx)
			}
			_ = process.Close()
			_ = waitProcess(runCtx, process)
			return count, ErrConnectorProtocol
		}
		ingressRequest, err := request.ToIngressRequest()
		if err != nil {
			_ = process.Close()
			_ = waitProcess(runCtx, process)
			return count, ErrConnectorProtocol
		}
		ingressACK, err := puller.Ingress.Accept(ingressRequest)
		if err != nil {
			_ = process.Close()
			_ = waitProcess(runCtx, process)
			return count, ErrConnectorProtocol
		}
		ack, err := relay.NewForwardACK(request, ingressACK, puller.now())
		if err != nil {
			_ = process.Close()
			_ = waitProcess(runCtx, process)
			return count, ErrConnectorProtocol
		}
		if err := writeForwardACK(runCtx, process, ack); err != nil {
			if contextError(runCtx) != nil {
				return count, contextError(runCtx)
			}
			_ = process.Close()
			_ = waitProcess(runCtx, process)
			return count, ErrConnectorProtocol
		}
	}
}

func (puller Puller) now() time.Time {
	if puller.Now != nil {
		return puller.Now().UTC()
	}
	return time.Now().UTC()
}

type requestResult struct {
	request relay.ForwardRequest
	err     error
}

func readForwardRequest(
	ctx context.Context,
	process Process,
) (relay.ForwardRequest, error) {
	result := make(chan requestResult, 1)
	go func() {
		request, err := relay.ReadForwardRequest(process.Stdout())
		result <- requestResult{request: request, err: err}
	}()
	select {
	case value := <-result:
		return value.request, value.err
	case <-ctx.Done():
		_ = process.Close()
		<-result
		return relay.ForwardRequest{}, ctx.Err()
	}
}

func writeForwardACK(
	ctx context.Context,
	process Process,
	ack relay.ForwardACK,
) error {
	result := make(chan error, 1)
	go func() {
		result <- relay.WriteForwardACK(process.Stdin(), ack)
	}()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		_ = process.Close()
		<-result
		return ctx.Err()
	}
}

func waitProcess(ctx context.Context, process Process) error {
	result := make(chan error, 1)
	go func() {
		result <- process.Wait()
	}()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		_ = process.Close()
		return ctx.Err()
	}
}

func contextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

var _ Runner = ExecRunner{}
var _ Process = (*execProcess)(nil)
var _ Ingress = relay.Ingress{}
