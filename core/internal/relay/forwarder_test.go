package relay

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type scriptedForwardTransport struct {
	calls    int
	requests []ForwardRequest
	send     func(context.Context, ForwardRequest) (ForwardACK, error)
}

func (transport *scriptedForwardTransport) Send(
	ctx context.Context,
	request ForwardRequest,
) (ForwardACK, error) {
	transport.calls++
	request.ExactBody = append([]byte(nil), request.ExactBody...)
	request.Signature.Signature = append(
		[]byte(nil),
		request.Signature.Signature...,
	)
	transport.requests = append(transport.requests, request)
	if transport.send == nil {
		return ForwardACK{}, errors.New("not scripted")
	}
	return transport.send(ctx, request)
}

type scriptedForwardOutbox struct {
	item      *OutboxItem
	claimErr  error
	ackErr    error
	nackErr   error
	ackCalls  int
	nackCalls int
}

func (outbox *scriptedForwardOutbox) Claim(
	time.Time,
	time.Duration,
) (*OutboxItem, error) {
	return outbox.item, outbox.claimErr
}

func (outbox *scriptedForwardOutbox) Ack(
	*OutboxItem,
	time.Time,
) error {
	outbox.ackCalls++
	return outbox.ackErr
}

func (outbox *scriptedForwardOutbox) Nack(
	*OutboxItem,
	error,
	time.Time,
	[]time.Duration,
) (OutboxState, error) {
	outbox.nackCalls++
	return OutboxPending, outbox.nackErr
}

func TestForwarderClaimsSendsAndAcksOnlyDurableReceipt(t *testing.T) {
	outbox, now, id := enqueuedOutbox(t)
	transport := &scriptedForwardTransport{
		send: func(_ context.Context, request ForwardRequest) (ForwardACK, error) {
			item := &OutboxItem{
				ID:          request.ItemID,
				DeliveryKey: request.DeliveryKey,
				BodyDigest:  request.BodyDigest,
			}
			return validForwardACK(item), nil
		},
	}
	forwarder := Forwarder{
		Outbox:    outbox,
		Transport: transport,
		Now:       func() time.Time { return now },
		Backoff:   []time.Duration{time.Second, time.Minute},
	}
	forwarded, err := forwarder.ForwardOne(context.Background())
	if err != nil || !forwarded {
		t.Fatalf("forwarded=%v err=%v", forwarded, err)
	}
	if transport.calls != 1 || len(transport.requests) != 1 {
		t.Fatalf("transport calls = %d", transport.calls)
	}
	if _, err := outbox.read(OutboxHistory, id); err != nil {
		t.Fatalf("history ACK was not persisted: %v", err)
	}
	if _, err := os.Stat(outbox.path(OutboxInflight, id)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inflight item survived ACK: %v", err)
	}
}

func TestForwarderNacksInvalidACKWithBackoff(t *testing.T) {
	outbox, now, id := enqueuedOutbox(t)
	transport := &scriptedForwardTransport{
		send: func(_ context.Context, request ForwardRequest) (ForwardACK, error) {
			item := &OutboxItem{
				ID:          request.ItemID,
				DeliveryKey: request.DeliveryKey,
				BodyDigest:  request.BodyDigest,
			}
			ack := validForwardACK(item)
			ack.ReceiptID = "wrong"
			return ack, nil
		},
	}
	backoff := 15 * time.Second
	forwarder := Forwarder{
		Outbox:    outbox,
		Transport: transport,
		Now:       func() time.Time { return now },
		Backoff:   []time.Duration{backoff, time.Minute},
	}
	forwarded, err := forwarder.ForwardOne(context.Background())
	if !forwarded || !errors.Is(err, ErrInvalidForwardACK) {
		t.Fatalf("forwarded=%v err=%v", forwarded, err)
	}
	pending, err := outbox.read(OutboxPending, id)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Attempts != 1 ||
		!pending.NextAttemptAt.Equal(now.Add(backoff)) ||
		pending.LastError != ErrInvalidForwardACK.Error() {
		t.Fatalf("pending retry = %#v", pending)
	}
}

func TestForwarderNacksTransportFailureWithoutPersistingSecrets(t *testing.T) {
	outbox, now, id := enqueuedOutbox(t)
	secret := "private body token oc_secret"
	transport := &scriptedForwardTransport{
		send: func(context.Context, ForwardRequest) (ForwardACK, error) {
			return ForwardACK{}, errors.New(secret)
		},
	}
	forwarder := Forwarder{
		Outbox:    outbox,
		Transport: transport,
		Now:       func() time.Time { return now },
		Backoff:   []time.Duration{time.Second, time.Minute},
	}
	forwarded, err := forwarder.ForwardOne(context.Background())
	if !forwarded || !errors.Is(err, ErrForwardTransport) {
		t.Fatalf("forwarded=%v err=%v", forwarded, err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("forwarder error leaked transport detail: %v", err)
	}
	pending, readErr := outbox.read(OutboxPending, id)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(pending.LastError, secret) ||
		pending.LastError != ErrForwardTransport.Error() {
		t.Fatalf("persisted error leaked transport detail: %q", pending.LastError)
	}
}

func TestForwarderGracefulEOFAndCancellationReturnItemToPending(t *testing.T) {
	tests := []struct {
		name      string
		ctx       func() context.Context
		sendError error
	}{
		{
			name:      "EOF",
			ctx:       context.Background,
			sendError: io.EOF,
		},
		{
			name: "canceled",
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			sendError: context.Canceled,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outbox, now, id := enqueuedOutbox(t)
			transport := &scriptedForwardTransport{
				send: func(context.Context, ForwardRequest) (ForwardACK, error) {
					return ForwardACK{}, test.sendError
				},
			}
			forwarder := Forwarder{
				Outbox:    outbox,
				Transport: transport,
				Now:       func() time.Time { return now },
				Backoff:   []time.Duration{time.Second, time.Minute},
			}
			if test.name == "EOF" {
				if err := forwarder.Run(test.ctx()); err != nil {
					t.Fatalf("graceful EOF = %v", err)
				}
			} else {
				// A context canceled before Claim is a clean no-op.
				if err := forwarder.Run(test.ctx()); err != nil {
					t.Fatalf("graceful cancellation = %v", err)
				}
			}
			if _, err := outbox.read(OutboxPending, id); err != nil {
				t.Fatalf("pending item missing: %v", err)
			}
		})
	}
}

func TestForwarderCancellationAfterClaimNacksItem(t *testing.T) {
	outbox, now, id := enqueuedOutbox(t)
	transport := &scriptedForwardTransport{
		send: func(ctx context.Context, _ ForwardRequest) (ForwardACK, error) {
			<-ctx.Done()
			return ForwardACK{}, ctx.Err()
		},
	}
	forwarder := Forwarder{
		Outbox:    outbox,
		Transport: transport,
		Now:       func() time.Time { return now },
		Backoff:   []time.Duration{time.Second, time.Minute},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	forwarded, err := forwarder.ForwardOne(ctx)
	if !forwarded || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("forwarded=%v err=%v", forwarded, err)
	}
	pending, err := outbox.read(OutboxPending, id)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Attempts != 1 ||
		pending.LastError != context.DeadlineExceeded.Error() {
		t.Fatalf("canceled claim was not nacked: %#v", pending)
	}
}

func TestForwarderRunDrainsReadyItemsAndStopsWhenEmpty(t *testing.T) {
	outbox, now, _ := enqueuedOutbox(t)
	transport := &scriptedForwardTransport{
		send: func(_ context.Context, request ForwardRequest) (ForwardACK, error) {
			return validForwardACK(&OutboxItem{
				ID:          request.ItemID,
				DeliveryKey: request.DeliveryKey,
				BodyDigest:  request.BodyDigest,
			}), nil
		},
	}
	forwarder := Forwarder{
		Outbox:    outbox,
		Transport: transport,
		Now:       func() time.Time { return now },
	}
	if err := forwarder.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if transport.calls != 1 {
		t.Fatalf("send calls = %d", transport.calls)
	}
	if forwarded, err := forwarder.ForwardOne(context.Background()); err != nil || forwarded {
		t.Fatalf("empty forward: forwarded=%v err=%v", forwarded, err)
	}
}

func TestForwarderACKPersistenceFailureDoesNotNackDurableRemoteReceipt(t *testing.T) {
	item := claimedOutboxItem(t)
	queue := &scriptedForwardOutbox{
		item:   item,
		ackErr: errors.New("disk unavailable"),
	}
	transport := &scriptedForwardTransport{
		send: func(_ context.Context, request ForwardRequest) (ForwardACK, error) {
			return validForwardACK(&OutboxItem{
				ID:          request.ItemID,
				DeliveryKey: request.DeliveryKey,
				BodyDigest:  request.BodyDigest,
			}), nil
		},
	}
	forwarder := Forwarder{Outbox: queue, Transport: transport}
	forwarded, err := forwarder.ForwardOne(context.Background())
	if !forwarded || !errors.Is(err, ErrForwardPersistence) {
		t.Fatalf("forwarded=%v err=%v", forwarded, err)
	}
	if queue.ackCalls != 1 || queue.nackCalls != 0 {
		t.Fatalf("ack calls=%d nack calls=%d", queue.ackCalls, queue.nackCalls)
	}
}

func TestForwarderPersistenceFailuresAndInvalidTiming(t *testing.T) {
	item := claimedOutboxItem(t)
	transport := &scriptedForwardTransport{
		send: func(context.Context, ForwardRequest) (ForwardACK, error) {
			return ForwardACK{}, errors.New("offline")
		},
	}
	tests := []struct {
		name      string
		forwarder Forwarder
	}{
		{
			"claim",
			Forwarder{
				Outbox:    &scriptedForwardOutbox{claimErr: errors.New("disk")},
				Transport: transport,
			},
		},
		{
			"nack",
			Forwarder{
				Outbox: &scriptedForwardOutbox{
					item:    item,
					nackErr: errors.New("disk"),
				},
				Transport: transport,
			},
		},
		{
			"negative lease",
			Forwarder{
				Outbox:    &scriptedForwardOutbox{},
				Transport: transport,
				Lease:     -time.Second,
			},
		},
		{
			"invalid backoff",
			Forwarder{
				Outbox:    &scriptedForwardOutbox{},
				Transport: transport,
				Backoff:   []time.Duration{0},
			},
		},
		{
			"zero clock",
			Forwarder{
				Outbox:    &scriptedForwardOutbox{},
				Transport: transport,
				Now:       func() time.Time { return time.Time{} },
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.forwarder.ForwardOne(context.Background())
			if err == nil {
				t.Fatal("invalid/persistence state returned success")
			}
		})
	}
}

func TestForwarderValidatesDependencies(t *testing.T) {
	if _, err := (Forwarder{}).ForwardOne(context.Background()); err == nil {
		t.Fatal("incomplete forwarder accepted")
	}
	if err := (Forwarder{}).Run(context.Background()); err == nil {
		t.Fatal("incomplete forwarder run accepted")
	}
}

func claimedOutboxItem(t *testing.T) *OutboxItem {
	t.Helper()
	outbox, now, _ := enqueuedOutbox(t)
	item, err := outbox.Claim(now, time.Minute)
	if err != nil || item == nil {
		t.Fatalf("claim: item=%#v err=%v", item, err)
	}
	return item
}

func enqueuedOutbox(t *testing.T) (*Outbox, time.Time, string) {
	t.Helper()
	outbox, err := OpenOutbox(filepath.Join(t.TempDir(), "outbox"))
	if err != nil {
		t.Fatal(err)
	}
	body, signature := signedOutboxInput(t)
	now := time.Now().UTC().Truncate(time.Second)
	id, _, err := outbox.Enqueue(body, signature, now)
	if err != nil {
		t.Fatal(err)
	}
	return outbox, now, id
}

func validForwardACK(item *OutboxItem) ForwardACK {
	return ForwardACK{
		ItemID:       item.ID,
		DeliveryKey:  item.DeliveryKey,
		BodyDigest:   item.BodyDigest,
		ReceiptID:    item.ID,
		LocalQueueID: "local-queue-item",
		Durable:      true,
		CommittedAt:  time.Now().UTC(),
	}
}
