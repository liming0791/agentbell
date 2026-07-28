package remote

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/liming0791/agentbell/core/internal/relay"
)

func TestDrainOutboxUsesSharedForwarderAndACKProtocol(t *testing.T) {
	request := validForwardRequest(t)
	outbox, err := relay.OpenOutbox(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 27, 13, 0, 0, 0, time.UTC)
	if _, _, err := outbox.Enqueue(
		request.ExactBody,
		request.Signature,
		now,
	); err != nil {
		t.Fatal(err)
	}
	drainInput, hostOutput := io.Pipe()
	hostInput, drainOutput := io.Pipe()
	hostDone := make(chan error, 1)
	go func() {
		forwarded, err := relay.ReadForwardRequest(hostInput)
		if err != nil {
			hostDone <- err
			return
		}
		ack := relay.ForwardACK{
			ItemID:       forwarded.ItemID,
			DeliveryKey:  forwarded.DeliveryKey,
			BodyDigest:   forwarded.BodyDigest,
			ReceiptID:    forwarded.ItemID,
			LocalQueueID: "host-queue-one",
			Durable:      true,
			CommittedAt:  now.Add(time.Second),
		}
		hostDone <- relay.WriteForwardACK(hostOutput, ack)
	}()

	count, err := DrainOutbox(
		context.Background(),
		outbox,
		drainInput,
		drainOutput,
		DrainOptions{
			Now:      func() time.Time { return now },
			Lease:    time.Minute,
			MaxItems: 4,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("count = %d", count)
	}
	if err := <-hostDone; err != nil {
		t.Fatal(err)
	}
	if item, err := outbox.Claim(now.Add(time.Hour), time.Minute); err != nil || item != nil {
		t.Fatalf("outbox was not drained: item=%#v err=%v", item, err)
	}
}

func TestDrainOutboxFailureIsSanitizedAndPersistsBackoff(t *testing.T) {
	request := validForwardRequest(t)
	outbox, err := relay.OpenOutbox(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 27, 13, 0, 0, 0, time.UTC)
	if _, _, err := outbox.Enqueue(
		request.ExactBody,
		request.Signature,
		now,
	); err != nil {
		t.Fatal(err)
	}
	drainInput, hostOutput := io.Pipe()
	hostInput, drainOutput := io.Pipe()
	secret := "private remote diagnostic"
	go func() {
		_, _ = relay.ReadForwardRequest(hostInput)
		_ = hostOutput.CloseWithError(errors.New(secret))
	}()
	count, err := DrainOutbox(
		context.Background(),
		outbox,
		drainInput,
		drainOutput,
		DrainOptions{
			Now:      func() time.Time { return now },
			Lease:    time.Minute,
			Backoff:  []time.Duration{time.Second},
			MaxItems: 1,
		},
	)
	if count != 1 || !errors.Is(err, ErrDrainFailed) {
		t.Fatalf("count=%d error=%v", count, err)
	}
	if containsAny(err.Error(), secret, string(request.ExactBody)) {
		t.Fatalf("drain error leaked details: %v", err)
	}
	if item, claimErr := outbox.Claim(now, time.Minute); claimErr != nil || item != nil {
		t.Fatalf("backoff was not persisted: item=%#v err=%v", item, claimErr)
	}
}

func TestDrainOutboxValidatesInputsAndCancellation(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	if _, err := DrainOutbox(
		context.Background(),
		nil,
		reader,
		writer,
		DrainOptions{},
	); !errors.Is(err, ErrInvalidDrain) {
		t.Fatalf("nil outbox error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	outbox, err := relay.OpenOutbox(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DrainOutbox(
		ctx,
		outbox,
		reader,
		writer,
		DrainOptions{},
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}
