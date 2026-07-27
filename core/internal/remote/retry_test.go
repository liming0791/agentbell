package remote

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type scriptedRunner struct {
	mutex     sync.Mutex
	processes []Process
	errors    []error
	calls     int
}

func (runner *scriptedRunner) Start(
	_ context.Context,
	_ CommandSpec,
) (Process, error) {
	runner.mutex.Lock()
	defer runner.mutex.Unlock()
	index := runner.calls
	runner.calls++
	var process Process
	var err error
	if index < len(runner.processes) {
		process = runner.processes[index]
	}
	if index < len(runner.errors) {
		err = runner.errors[index]
	}
	return process, err
}

func TestPullerRunRetriesWithExactBackoffThenSucceeds(t *testing.T) {
	process, remoteOutput, _ := newFakeProcess()
	go func() {
		_ = remoteOutput.Close()
		process.waitResult <- nil
	}()
	runner := &scriptedRunner{
		processes: []Process{nil, nil, process},
		errors: []error{
			errors.New("offline secret one"),
			errors.New("offline secret two"),
			nil,
		},
	}
	var delays []time.Duration
	total, err := (Puller{
		Runner:  runner,
		Ingress: &ingressStub{},
		Timeout: time.Second,
	}).Run(
		context.Background(),
		validRemoteConfig("wsl"),
		RetryOptions{
			Backoff:     []time.Duration{time.Second, 5 * time.Second},
			MaxAttempts: 3,
			Wait: func(_ context.Context, delay time.Duration) error {
				delays = append(delays, delay)
				return nil
			},
		},
	)
	if err != nil || total != 0 {
		t.Fatalf("total=%d error=%v", total, err)
	}
	if runner.calls != 3 {
		t.Fatalf("runner calls = %d", runner.calls)
	}
	if len(delays) != 2 ||
		delays[0] != time.Second ||
		delays[1] != 5*time.Second {
		t.Fatalf("delays = %#v", delays)
	}
}

func TestPullerRunStopsOnCancellationAndPermanentConfiguration(t *testing.T) {
	runner := &scriptedRunner{
		errors: []error{errors.New("offline"), errors.New("offline")},
	}
	ctx, cancel := context.WithCancel(context.Background())
	_, err := (Puller{
		Runner:  runner,
		Ingress: &ingressStub{},
		Timeout: time.Second,
	}).Run(
		ctx,
		validRemoteConfig("ssh"),
		RetryOptions{
			MaxAttempts: 3,
			Backoff:     []time.Duration{time.Second},
			Wait: func(context.Context, time.Duration) error {
				cancel()
				return context.Canceled
			},
		},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	if runner.calls != 1 {
		t.Fatalf("runner calls after cancellation = %d", runner.calls)
	}

	runner = &scriptedRunner{}
	_, err = (Puller{
		Runner:  runner,
		Ingress: &ingressStub{},
	}).Run(
		context.Background(),
		validRemoteConfig("https"),
		RetryOptions{MaxAttempts: 3},
	)
	if !errors.Is(err, ErrNotPullConnector) || runner.calls != 0 {
		t.Fatalf("permanent error=%v runner calls=%d", err, runner.calls)
	}
}

func TestPullerRunValidatesRetryPolicyAndExhaustion(t *testing.T) {
	puller := Puller{
		Runner: &scriptedRunner{
			errors: []error{errors.New("secret one"), errors.New("secret two")},
		},
		Ingress: &ingressStub{},
		Timeout: time.Second,
	}
	for _, options := range []RetryOptions{
		{MaxAttempts: -1},
		{Backoff: []time.Duration{0}},
	} {
		if _, err := puller.Run(
			context.Background(),
			validRemoteConfig("container"),
			options,
		); !errors.Is(err, ErrInvalidRetryPolicy) {
			t.Fatalf("options=%#v error=%v", options, err)
		}
	}
	_, err := puller.Run(
		context.Background(),
		validRemoteConfig("container"),
		RetryOptions{
			MaxAttempts: 2,
			Backoff:     []time.Duration{time.Millisecond},
			Wait: func(context.Context, time.Duration) error {
				return nil
			},
		},
	)
	if !errors.Is(err, ErrConnectorRetriesExhausted) {
		t.Fatalf("exhaustion error = %v", err)
	}
	if containsAny(err.Error(), "secret one", "secret two") {
		t.Fatalf("retry error leaked details: %v", err)
	}
}

func TestWaitContextHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitContext(ctx, time.Hour); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("wait error = %v", err)
	}
}
