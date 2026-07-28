package remote

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/liming0791/agentbell/core/internal/relay"
)

const testPairingCode = "AGBR-01234567-89ABCDEF-GHJKMNPQ-RSTVWXYZ"

func TestBuildPairCommandUsesExactConnectorArgv(t *testing.T) {
	tests := []struct {
		kind string
		args []string
	}{
		{
			kind: "wsl",
			args: []string{
				"-d", "Ubuntu-24.04",
				"--exec", "/usr/local/bin/agentbell",
				"remote", "pair", "--stdio",
			},
		},
		{
			kind: "ssh",
			args: []string{
				"-T",
				"-o", "BatchMode=yes",
				"-o", "StrictHostKeyChecking=yes",
				"-o", "UserKnownHostsFile=/Users/test/.ssh/known_hosts",
				"-p", "2222",
				"--", "agentbell@build.example.com",
				"/usr/local/bin/agentbell", "remote", "pair", "--stdio",
			},
		},
		{
			kind: "container",
			args: []string{
				"exec", "-i", "--", "worker-01",
				"/usr/local/bin/agentbell", "remote", "pair", "--stdio",
			},
		},
	}
	for _, test := range tests {
		spec, err := BuildPairCommand(validRemoteConfig(test.kind))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(spec.Arguments, test.args) {
			t.Fatalf("%s args = %#v", test.kind, spec.Arguments)
		}
		if containsAny(spec.String(), testPairingCode) {
			t.Fatal("pair command formatting leaked a pairing code")
		}
	}
	if _, err := BuildPairCommand(validRemoteConfig("https")); !errors.Is(err, ErrNotPullConnector) {
		t.Fatalf("HTTPS pair command error = %v", err)
	}
}

func TestStdioPairerEnrollsAndWaitsForCleanChildExit(t *testing.T) {
	config := validRemoteConfig("wsl")
	hello := validPairHello()
	hello.TeamID = config.TeamID
	hello.OriginID = config.OriginID
	hello.Runtime = config.Runtime
	process, remoteOutput, remoteInput := newFakeProcess()
	runner := &fakeRunner{process: process}
	var captured relay.PairEnrollmentRequest
	childDone := make(chan error, 1)
	go func() {
		if err := WritePairHello(remoteOutput, hello); err != nil {
			childDone <- err
			return
		}
		decision, err := ReadPairDecision(remoteInput)
		if err == nil && (!decision.Accepted ||
			decision.PeerID != hello.PeerID ||
			decision.TeamID != hello.TeamID) {
			err = errors.New("unexpected decision")
		}
		_ = remoteOutput.Close()
		process.waitResult <- nil
		childDone <- err
	}()

	decision, err := (StdioPairer{
		Runner: runner,
		Enroll: func(
			ctx context.Context,
			request relay.PairEnrollmentRequest,
		) (relay.PairEnrollmentResult, error) {
			if ctx == nil {
				t.Fatal("nil enrollment context")
			}
			captured = request
			return relay.PairEnrollmentResult{
				PeerID:          request.PeerID,
				TeamID:          config.TeamID,
				AllowedSources:  []string{"codex", "claude"},
				AllowedRuntimes: []string{"wsl", "ssh"},
			}, nil
		},
		Timeout: time.Second,
	}).Pair(context.Background(), config, testPairingCode)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Accepted || captured.Code != testPairingCode ||
		captured.PeerID != hello.PeerID ||
		captured.OriginID != hello.OriginID ||
		!bytes.Equal(captured.PublicKey, hello.PublicKey) {
		t.Fatalf("decision=%#v enrollment=%#v", decision, captured)
	}
	if containsAny(
		runner.spec.Executable+" "+strings.Join(runner.spec.Arguments, " "),
		testPairingCode,
	) {
		t.Fatal("pairing code leaked into connector argv")
	}
	if err := <-childDone; err != nil {
		t.Fatal(err)
	}
}

func TestStdioPairerUsesHostConnectorRegistryTarget(t *testing.T) {
	target := schedulerTarget("container")
	spec, err := BuildPairCommandForConnector(
		target.Runtime,
		target.Connector,
	)
	if err != nil ||
		spec.Arguments[len(spec.Arguments)-2] != "pair" {
		t.Fatalf("spec=%#v err=%v", spec, err)
	}
	hello := validPairHello()
	hello.TeamID = target.TeamID
	hello.OriginID = target.OriginID
	hello.Runtime = target.Runtime
	process, remoteOutput, remoteInput := newFakeProcess()
	childDone := make(chan error, 1)
	go func() {
		if err := WritePairHello(remoteOutput, hello); err != nil {
			childDone <- err
			return
		}
		_, err := ReadPairDecision(remoteInput)
		_ = remoteOutput.Close()
		process.waitResult <- nil
		childDone <- err
	}()
	decision, err := (StdioPairer{
		Runner: &fakeRunner{process: process},
		Enroll: func(
			_ context.Context,
			request relay.PairEnrollmentRequest,
		) (relay.PairEnrollmentResult, error) {
			return relay.PairEnrollmentResult{
				PeerID:          request.PeerID,
				TeamID:          target.TeamID,
				AllowedSources:  []string{"codex"},
				AllowedRuntimes: []string{"container"},
			}, nil
		},
		Timeout: time.Second,
	}).PairConnector(context.Background(), target, testPairingCode)
	if err != nil || !decision.Accepted {
		t.Fatalf("decision=%#v err=%v", decision, err)
	}
	if err := <-childDone; err != nil {
		t.Fatal(err)
	}
}

func TestStdioPairerRejectsInvalidHelloAndEnrollmentFailure(t *testing.T) {
	config := validRemoteConfig("wsl")
	tests := []struct {
		name      string
		hello     PairHello
		enroll    relay.PairEnrollmentFunc
		errorCode string
		want      error
	}{
		{
			name: "connector mismatch",
			hello: func() PairHello {
				value := validPairHello()
				value.TeamID = "team-other"
				return value
			}(),
			enroll: func(context.Context, relay.PairEnrollmentRequest) (
				relay.PairEnrollmentResult,
				error,
			) {
				return relay.PairEnrollmentResult{}, errors.New("must not run")
			},
			errorCode: PairErrorInvalidHello,
			want:      ErrPairProtocol,
		},
		{
			name:  "enrollment rejected",
			hello: validPairHello(),
			enroll: func(context.Context, relay.PairEnrollmentRequest) (
				relay.PairEnrollmentResult,
				error,
			) {
				return relay.PairEnrollmentResult{}, errors.New("database exposed a secret")
			},
			errorCode: PairErrorEnrollmentFailed,
			want:      ErrPairEnrollment,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.hello.TeamID = config.TeamID
			test.hello.OriginID = config.OriginID
			test.hello.Runtime = config.Runtime
			if test.name == "connector mismatch" {
				test.hello.TeamID = "team-other"
			}
			process, remoteOutput, remoteInput := newFakeProcess()
			childDone := make(chan error, 1)
			go func() {
				if err := WritePairHello(remoteOutput, test.hello); err != nil {
					childDone <- err
					return
				}
				decision, err := ReadPairDecision(remoteInput)
				if err == nil && (decision.Accepted ||
					decision.ErrorCode != test.errorCode) {
					err = errors.New("unexpected rejection")
				}
				_ = remoteOutput.Close()
				process.waitResult <- nil
				childDone <- err
			}()
			_, err := (StdioPairer{
				Runner:  &fakeRunner{process: process},
				Enroll:  test.enroll,
				Timeout: time.Second,
			}).Pair(context.Background(), config, testPairingCode)
			if !errors.Is(err, test.want) ||
				strings.Contains(err.Error(), "database") ||
				strings.Contains(err.Error(), testPairingCode) {
				t.Fatalf("Pair error = %v", err)
			}
			if err := <-childDone; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStdioPairerRejectsMalformedFrameBestEffort(t *testing.T) {
	config := validRemoteConfig("wsl")
	process, remoteOutput, remoteInput := newFakeProcess()
	childDone := make(chan error, 1)
	go func() {
		if _, err := io.WriteString(remoteOutput, "bad-frame-12"); err != nil {
			childDone <- err
			return
		}
		decision, err := ReadPairDecision(remoteInput)
		if err == nil && (decision.Accepted ||
			decision.ErrorCode != PairErrorInvalidHello) {
			err = errors.New("unexpected rejection")
		}
		_ = remoteOutput.Close()
		process.waitResult <- nil
		childDone <- err
	}()
	_, err := (StdioPairer{
		Runner: &fakeRunner{process: process},
		Enroll: func(context.Context, relay.PairEnrollmentRequest) (
			relay.PairEnrollmentResult,
			error,
		) {
			return relay.PairEnrollmentResult{}, errors.New("must not run")
		},
		Timeout: time.Second,
	}).Pair(context.Background(), config, testPairingCode)
	if !errors.Is(err, ErrPairProtocol) {
		t.Fatalf("malformed pair error = %v", err)
	}
	if err := <-childDone; err != nil {
		t.Fatal(err)
	}
}

func TestStdioPairerValidatesDependenciesStartAndExit(t *testing.T) {
	config := validRemoteConfig("wsl")
	pairerText := (StdioPairer{}).String() + (StdioPairer{}).GoString()
	if containsAny(pairerText, testPairingCode, config.TeamID, config.OriginID) {
		t.Fatalf("pairer formatting leaked state: %q", pairerText)
	}
	if _, err := (StdioPairer{}).Pair(
		nil,
		config,
		testPairingCode,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("nil context error = %v", err)
	}
	if _, err := (StdioPairer{}).Pair(
		context.Background(),
		config,
		testPairingCode,
	); !errors.Is(err, ErrInvalidPairer) {
		t.Fatalf("missing enroll error = %v", err)
	}
	if _, err := (StdioPairer{
		Enroll: func(context.Context, relay.PairEnrollmentRequest) (relay.PairEnrollmentResult, error) {
			return relay.PairEnrollmentResult{}, nil
		},
		Timeout: -1,
	}).Pair(context.Background(), config, testPairingCode); !errors.Is(err, ErrInvalidPairer) {
		t.Fatalf("negative timeout error = %v", err)
	}
	if _, err := (StdioPairer{
		Runner: &fakeRunner{err: errors.New("secret process error")},
		Enroll: func(context.Context, relay.PairEnrollmentRequest) (
			relay.PairEnrollmentResult,
			error,
		) {
			return relay.PairEnrollmentResult{}, nil
		},
	}).Pair(context.Background(), config, testPairingCode); !errors.Is(err, ErrConnectorStart) {
		t.Fatalf("start error = %v", err)
	}

	process, remoteOutput, remoteInput := newFakeProcess()
	go func() {
		hello := validPairHello()
		hello.TeamID = config.TeamID
		hello.OriginID = config.OriginID
		hello.Runtime = config.Runtime
		_ = WritePairHello(remoteOutput, hello)
		_, _ = ReadPairDecision(remoteInput)
		_ = remoteOutput.Close()
		process.waitResult <- errors.New("child printed " + testPairingCode)
	}()
	_, err := (StdioPairer{
		Runner: &fakeRunner{process: process},
		Enroll: func(
			_ context.Context,
			request relay.PairEnrollmentRequest,
		) (relay.PairEnrollmentResult, error) {
			return relay.PairEnrollmentResult{
				PeerID:          request.PeerID,
				TeamID:          config.TeamID,
				AllowedSources:  []string{"codex"},
				AllowedRuntimes: []string{"wsl"},
			}, nil
		},
		Timeout: time.Second,
	}).Pair(context.Background(), config, testPairingCode)
	if !errors.Is(err, ErrConnectorExit) ||
		strings.Contains(err.Error(), testPairingCode) {
		t.Fatalf("child exit error = %v", err)
	}
}

func TestPairTypesDoNotAliasCallerBuffers(t *testing.T) {
	hello := validPairHello()
	var wire bytes.Buffer
	if err := WritePairHello(&wire, hello); err != nil {
		t.Fatal(err)
	}
	decoded, err := ReadPairHello(&wire)
	if err != nil {
		t.Fatal(err)
	}
	hello.PublicKey[0] ^= 0xff
	if decoded.PublicKey[0] == hello.PublicKey[0] {
		t.Fatal("decoded public key aliases caller data")
	}

	decision := validPairDecision()
	wire.Reset()
	if err := WritePairDecision(&wire, decision); err != nil {
		t.Fatal(err)
	}
	decodedDecision, err := ReadPairDecision(&wire)
	if err != nil {
		t.Fatal(err)
	}
	decision.AllowedSources[0] = "kimi"
	if reflect.DeepEqual(decodedDecision.AllowedSources, decision.AllowedSources) {
		t.Fatal("decoded decision aliases caller data")
	}
}
