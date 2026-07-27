package remote

import (
	"context"
	"crypto/ed25519"
	"errors"
	"regexp"
	"time"

	"github.com/liming0791/agentbell/core/internal/relay"
	"github.com/liming0791/agentbell/core/internal/remoteconfig"
)

const defaultPairTimeout = 2 * time.Minute

var (
	ErrInvalidPairer  = errors.New("remote stdio pairer dependencies are incomplete")
	ErrPairEnrollment = errors.New("remote pairing enrollment failed")
)

var stdioPairingCodePattern = regexp.MustCompile(
	`^AGBR-[0123456789ABCDEFGHJKMNPQRSTVWXYZ]{8}` +
		`(?:-[0123456789ABCDEFGHJKMNPQRSTVWXYZ]{8}){3}$`,
)

// BuildPairCommand returns the same connector invocation as host-pull with the
// exact remote subcommand changed to "remote pair --stdio". The bearer code is
// deliberately absent from this API and cannot enter argv.
func BuildPairCommand(config remoteconfig.RemoteConfig) (CommandSpec, error) {
	if err := config.Validate(); err != nil {
		return CommandSpec{}, ErrInvalidRemoteConfig
	}
	return BuildPairCommandForConnector(config.Runtime, config.Connector)
}

func BuildPairCommandForConnector(
	runtimeName string,
	connector remoteconfig.Connector,
) (CommandSpec, error) {
	spec, err := BuildPullCommandForConnector(runtimeName, connector)
	if err != nil {
		return CommandSpec{}, err
	}
	if len(spec.Arguments) < 3 ||
		spec.Arguments[len(spec.Arguments)-3] != "remote" ||
		spec.Arguments[len(spec.Arguments)-2] != "drain" ||
		spec.Arguments[len(spec.Arguments)-1] != "--stdio" {
		return CommandSpec{}, ErrInvalidRemoteConfig
	}
	spec.Arguments = append([]string(nil), spec.Arguments...)
	spec.Arguments[len(spec.Arguments)-2] = "pair"
	return spec, nil
}

// StdioPairer performs one no-listener enrollment over a WSL, SSH or container
// process. The pairing code exists only as the Pair argument and in the
// in-memory PairEnrollmentRequest passed to Enroll.
type StdioPairer struct {
	Runner  Runner
	Enroll  relay.PairEnrollmentFunc
	Timeout time.Duration
}

func (pairer StdioPairer) String() string {
	return "remote.StdioPairer{<redacted>}"
}

func (pairer StdioPairer) GoString() string {
	return pairer.String()
}

func (pairer StdioPairer) Pair(
	ctx context.Context,
	config remoteconfig.RemoteConfig,
	code string,
) (PairDecision, error) {
	if err := config.Validate(); err != nil {
		return PairDecision{}, ErrInvalidRemoteConfig
	}
	spec, err := BuildPairCommandForConnector(
		config.Runtime,
		config.Connector,
	)
	if err != nil {
		return PairDecision{}, err
	}
	return pairer.pair(
		ctx,
		spec,
		config.TeamID,
		config.OriginID,
		config.Runtime,
		code,
	)
}

func (pairer StdioPairer) PairConnector(
	ctx context.Context,
	target remoteconfig.HostConnector,
	code string,
) (PairDecision, error) {
	if err := target.Validate(); err != nil {
		return PairDecision{}, ErrInvalidRemoteConfig
	}
	spec, err := BuildPairCommandForConnector(
		target.Runtime,
		target.Connector,
	)
	if err != nil {
		return PairDecision{}, err
	}
	return pairer.pair(
		ctx,
		spec,
		target.TeamID,
		target.OriginID,
		target.Runtime,
		code,
	)
}

func (pairer StdioPairer) pair(
	ctx context.Context,
	spec CommandSpec,
	teamID string,
	originID string,
	runtimeName string,
	code string,
) (PairDecision, error) {
	if ctx == nil {
		return PairDecision{}, context.Canceled
	}
	if pairer.Enroll == nil ||
		pairer.Timeout < 0 ||
		!stdioPairingCodePattern.MatchString(code) {
		return PairDecision{}, ErrInvalidPairer
	}
	if err := ctx.Err(); err != nil {
		return PairDecision{}, err
	}
	timeout := pairer.Timeout
	if timeout == 0 {
		timeout = defaultPairTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	runner := pairer.Runner
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
		return PairDecision{}, ErrConnectorStart
	}
	defer process.Close()

	hello, err := readPairHelloContext(runCtx, process)
	if err != nil {
		if contextError(runCtx) != nil {
			return PairDecision{}, contextError(runCtx)
		}
		pairer.rejectAndFinish(
			runCtx,
			process,
			PairErrorInvalidHello,
		)
		return PairDecision{}, ErrPairProtocol
	}
	if !helloMatchesExpected(
		hello,
		teamID,
		originID,
		runtimeName,
	) {
		pairer.rejectAndFinish(
			runCtx,
			process,
			PairErrorInvalidHello,
		)
		return PairDecision{}, ErrPairProtocol
	}
	request := relay.PairEnrollmentRequest{
		Code:      code,
		PeerID:    hello.PeerID,
		OriginID:  hello.OriginID,
		PublicKey: append(ed25519.PublicKey(nil), hello.PublicKey...),
	}
	result, enrollErr := callStdioEnrollment(
		pairer.Enroll,
		runCtx,
		request,
	)
	decision := PairDecision{
		Accepted:        true,
		PeerID:          result.PeerID,
		TeamID:          result.TeamID,
		AllowedSources:  append([]string(nil), result.AllowedSources...),
		AllowedRuntimes: append([]string(nil), result.AllowedRuntimes...),
	}
	if enrollErr != nil ||
		decision.validate() != nil ||
		decision.PeerID != hello.PeerID ||
		decision.TeamID != teamID {
		pairer.rejectAndFinish(
			runCtx,
			process,
			PairErrorEnrollmentFailed,
		)
		return PairDecision{}, ErrPairEnrollment
	}
	if err := writePairDecisionContext(runCtx, process, decision); err != nil {
		if contextError(runCtx) != nil {
			return PairDecision{}, contextError(runCtx)
		}
		_ = process.Stdin().Close()
		_ = waitProcess(runCtx, process)
		return PairDecision{}, ErrPairProtocol
	}
	if err := process.Stdin().Close(); err != nil {
		_ = waitProcess(runCtx, process)
		return PairDecision{}, ErrPairProtocol
	}
	if err := waitProcess(runCtx, process); err != nil {
		if contextError(runCtx) != nil {
			return PairDecision{}, contextError(runCtx)
		}
		return PairDecision{}, ErrConnectorExit
	}
	return decision, nil
}

func helloMatchesConfig(
	hello PairHello,
	config remoteconfig.RemoteConfig,
) bool {
	return helloMatchesExpected(
		hello,
		config.TeamID,
		config.OriginID,
		config.Runtime,
	)
}

func helloMatchesExpected(
	hello PairHello,
	teamID string,
	originID string,
	runtimeName string,
) bool {
	return hello.ProtocolVersion == PairProtocolVersion &&
		hello.TeamID == teamID &&
		hello.OriginID == originID &&
		hello.Runtime == runtimeName
}

func (pairer StdioPairer) rejectAndFinish(
	ctx context.Context,
	process Process,
	errorCode string,
) {
	_ = writePairDecisionContext(ctx, process, PairDecision{
		ErrorCode: errorCode,
	})
	_ = process.Stdin().Close()
	_ = waitProcess(ctx, process)
}

type pairHelloResult struct {
	hello PairHello
	err   error
}

func readPairHelloContext(
	ctx context.Context,
	process Process,
) (PairHello, error) {
	result := make(chan pairHelloResult, 1)
	go func() {
		hello, err := ReadPairHello(process.Stdout())
		result <- pairHelloResult{hello: hello, err: err}
	}()
	select {
	case value := <-result:
		return value.hello, value.err
	case <-ctx.Done():
		_ = process.Close()
		<-result
		return PairHello{}, ctx.Err()
	}
}

func writePairDecisionContext(
	ctx context.Context,
	process Process,
	decision PairDecision,
) error {
	result := make(chan error, 1)
	go func() {
		result <- WritePairDecision(process.Stdin(), decision)
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

func callStdioEnrollment(
	enroll relay.PairEnrollmentFunc,
	ctx context.Context,
	request relay.PairEnrollmentRequest,
) (
	result relay.PairEnrollmentResult,
	err error,
) {
	defer func() {
		if recover() != nil {
			result = relay.PairEnrollmentResult{}
			err = ErrPairEnrollment
		}
	}()
	return enroll(ctx, request)
}

var _ interface {
	String() string
	GoString() string
} = StdioPairer{}
