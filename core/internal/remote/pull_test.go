package remote

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/liming0791/agentbell/core/internal/event"
	"github.com/liming0791/agentbell/core/internal/relay"
)

type ingressStub struct {
	mutex     sync.Mutex
	requests  []relay.IngressRequest
	receiptID string
	err       error
}

func (stub *ingressStub) Accept(
	request relay.IngressRequest,
) (relay.IngressACK, error) {
	stub.mutex.Lock()
	defer stub.mutex.Unlock()
	stub.requests = append(stub.requests, request)
	if stub.err != nil {
		return relay.IngressACK{}, stub.err
	}
	return relay.IngressACK{
		ReceiptID:    stub.receiptID,
		LocalQueueID: "queue-one",
	}, nil
}

type fakeProcess struct {
	stdin      *io.PipeWriter
	stdout     *io.PipeReader
	closeOnce  sync.Once
	waitResult chan error
}

func (process *fakeProcess) Stdin() io.WriteCloser { return process.stdin }
func (process *fakeProcess) Stdout() io.ReadCloser { return process.stdout }
func (process *fakeProcess) Wait() error           { return <-process.waitResult }
func (process *fakeProcess) Close() error {
	process.closeOnce.Do(func() {
		_ = process.stdin.Close()
		_ = process.stdout.Close()
	})
	return nil
}

type fakeRunner struct {
	process Process
	spec    CommandSpec
	err     error
}

func (runner *fakeRunner) Start(
	_ context.Context,
	spec CommandSpec,
) (Process, error) {
	runner.spec = spec
	return runner.process, runner.err
}

func TestPullerReceivesFramesAndReturnsDurableACK(t *testing.T) {
	request := validForwardRequest(t)
	process, remoteOutput, remoteInput := newFakeProcess()
	runner := &fakeRunner{process: process}
	ingress := &ingressStub{receiptID: request.ItemID}
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)

	remoteDone := make(chan error, 1)
	go func() {
		if err := relay.WriteForwardRequest(remoteOutput, request); err != nil {
			remoteDone <- err
			return
		}
		ack, err := relay.ReadForwardACK(remoteInput)
		if err == nil {
			if ack.ItemID != request.ItemID ||
				ack.LocalQueueID != "queue-one" ||
				!ack.Durable ||
				!ack.CommittedAt.Equal(now) {
				err = errors.New("unexpected durable ACK")
			}
		}
		_ = remoteOutput.Close()
		process.waitResult <- nil
		remoteDone <- err
	}()

	count, err := (Puller{
		Runner:    runner,
		Ingress:   ingress,
		Now:       func() time.Time { return now },
		Timeout:   time.Second,
		MaxFrames: 4,
	}).Pull(context.Background(), validRemoteConfig("wsl"))
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("count = %d", count)
	}
	if err := <-remoteDone; err != nil {
		t.Fatal(err)
	}
	if runner.spec.Executable != `C:\Windows\System32\wsl.exe` {
		t.Fatalf("runner spec = %#v", runner.spec)
	}
	ingress.mutex.Lock()
	defer ingress.mutex.Unlock()
	if len(ingress.requests) != 1 {
		t.Fatalf("ingress requests = %d", len(ingress.requests))
	}
}

func TestPullerCancelsBlockedProcessAndSanitizesFailures(t *testing.T) {
	t.Run("cancellation closes process", func(t *testing.T) {
		process, _, _ := newFakeProcess()
		runner := &fakeRunner{process: process}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := (Puller{
			Runner:  runner,
			Ingress: &ingressStub{},
			Timeout: time.Second,
		}).Pull(ctx, validRemoteConfig("container"))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("ingress detail is not exposed", func(t *testing.T) {
		request := validForwardRequest(t)
		process, remoteOutput, _ := newFakeProcess()
		secret := "private-body-and-key"
		go func() {
			_ = relay.WriteForwardRequest(remoteOutput, request)
			_ = remoteOutput.Close()
			process.waitResult <- nil
		}()
		_, err := (Puller{
			Runner:  &fakeRunner{process: process},
			Ingress: &ingressStub{err: errors.New(secret)},
			Timeout: time.Second,
		}).Pull(context.Background(), validRemoteConfig("ssh"))
		if !errors.Is(err, ErrConnectorProtocol) {
			t.Fatalf("error = %v", err)
		}
		if containsAny(err.Error(), secret, string(request.ExactBody)) {
			t.Fatalf("secret leaked in error: %v", err)
		}
	})

	t.Run("start and exit errors are generic", func(t *testing.T) {
		_, err := (Puller{
			Runner:  &fakeRunner{err: errors.New("C:/secret/key")},
			Ingress: &ingressStub{},
			Timeout: time.Second,
		}).Pull(context.Background(), validRemoteConfig("wsl"))
		if !errors.Is(err, ErrConnectorStart) ||
			containsAny(err.Error(), "C:/secret/key") {
			t.Fatalf("start error = %v", err)
		}

		process, remoteOutput, _ := newFakeProcess()
		go func() {
			_ = remoteOutput.Close()
			process.waitResult <- errors.New("remote said secret")
		}()
		_, err = (Puller{
			Runner:  &fakeRunner{process: process},
			Ingress: &ingressStub{},
			Timeout: time.Second,
		}).Pull(context.Background(), validRemoteConfig("wsl"))
		if !errors.Is(err, ErrConnectorExit) ||
			containsAny(err.Error(), "remote said secret") {
			t.Fatalf("exit error = %v", err)
		}
	})
}

func TestPullerRejectsInvalidDependenciesAndFrameLimit(t *testing.T) {
	if _, err := (Puller{}).Pull(
		context.Background(),
		validRemoteConfig("wsl"),
	); !errors.Is(err, ErrInvalidPuller) {
		t.Fatalf("invalid puller error = %v", err)
	}
	process, remoteOutput, remoteInput := newFakeProcess()
	request := validForwardRequest(t)
	go func() {
		for index := 0; index < 2; index++ {
			if relay.WriteForwardRequest(remoteOutput, request) != nil {
				break
			}
			if _, err := relay.ReadForwardACK(remoteInput); err != nil {
				break
			}
		}
		process.waitResult <- nil
	}()
	count, err := (Puller{
		Runner:    &fakeRunner{process: process},
		Ingress:   &ingressStub{receiptID: request.ItemID},
		Timeout:   time.Second,
		MaxFrames: 1,
	}).Pull(context.Background(), validRemoteConfig("wsl"))
	if count != 1 || !errors.Is(err, ErrFrameLimit) {
		t.Fatalf("count=%d error=%v", count, err)
	}
}

func TestExecRunnerPassesArgumentsVerbatimWithoutShell(t *testing.T) {
	literal := `$(touch /tmp/must-not-exist); secret with spaces`
	process, err := (ExecRunner{}).Start(context.Background(), CommandSpec{
		Executable: os.Args[0],
		Arguments: []string{
			"-test.run=TestExecRunnerHelper",
			"--",
			"--remote-exec-helper",
			literal,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer process.Close()
	_ = process.Stdin().Close()
	body, err := io.ReadAll(process.Stdout())
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Wait(); err != nil {
		t.Fatal(err)
	}
	if string(body) != literal {
		t.Fatalf("child argv = %q", body)
	}

	if _, err := (ExecRunner{}).Start(
		context.Background(),
		CommandSpec{},
	); !errors.Is(err, ErrConnectorStart) {
		t.Fatalf("empty spec error = %v", err)
	}
}

func TestExecRunnerHelper(t *testing.T) {
	for index, argument := range os.Args {
		if argument == "--remote-exec-helper" && index+1 < len(os.Args) {
			_, _ = io.WriteString(os.Stdout, os.Args[index+1])
			os.Exit(0)
		}
	}
}

func newFakeProcess() (*fakeProcess, *io.PipeWriter, *io.PipeReader) {
	hostInput, remoteOutput := io.Pipe()
	remoteInput, hostOutput := io.Pipe()
	return &fakeProcess{
		stdin:      hostOutput,
		stdout:     hostInput,
		waitResult: make(chan error, 1),
	}, remoteOutput, remoteInput
}

func validForwardRequest(t *testing.T) relay.ForwardRequest {
	t.Helper()
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	notification := event.Notification{
		Version:        event.Version,
		Source:         "codex",
		Surface:        "cli",
		Runtime:        "wsl",
		Event:          event.EventTaskCompleted,
		Status:         event.StatusCompleted,
		OccurredAt:     now.Add(-time.Second),
		SessionID:      "sha256:0123456789abcdef",
		IdempotencyKey: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Priority:       event.PriorityNormal,
		PrivacyLevel:   event.PrivacyMetadataOnly,
		Project:        "agentbell",
	}
	deliveryKey, err := relay.DeriveDeliveryKey(
		"team-main",
		"origin-main",
		notification.IdempotencyKey,
	)
	if err != nil {
		t.Fatal(err)
	}
	envelope := relay.Envelope{
		ProtocolVersion: relay.ProtocolVersion,
		TeamID:          "team-main",
		Origin:          relay.Origin{ID: "origin-main", Runtime: "wsl"},
		Delivery: relay.Delivery{
			Key:         deliveryKey,
			ProducerKey: notification.IdempotencyKey,
		},
		SentAt: now,
		Nonce:  "0123456789abcdef0123456789abcdef",
		Event:  notification,
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := relay.Sign(
		privateKey,
		"POST",
		"/v1/events",
		now,
		envelope.Nonce,
		body,
	)
	if err != nil {
		t.Fatal(err)
	}
	outbox, err := relay.OpenOutbox(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := outbox.Enqueue(body, relay.SignatureMetadata{
		KeyID:     "peer-one",
		Method:    "POST",
		Target:    "/v1/events",
		SentAt:    now,
		Nonce:     envelope.Nonce,
		Signature: signature,
	}, now); err != nil {
		t.Fatal(err)
	}
	item, err := outbox.Claim(now, time.Minute)
	if err != nil || item == nil {
		t.Fatalf("claim: item=%#v err=%v", item, err)
	}
	request, err := relay.NewForwardRequest(item)
	if err != nil {
		t.Fatal(err)
	}
	return request
}
