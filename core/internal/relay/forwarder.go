package relay

import (
	"context"
	"errors"
	"io"
	"time"
)

var (
	ErrInvalidForwarder   = errors.New("relay forwarder dependencies are incomplete")
	ErrForwardTransport   = errors.New("relay forward transport failed")
	ErrForwardPersistence = errors.New("relay forward state persistence failed")
)

var defaultForwardBackoff = []time.Duration{
	time.Second,
	5 * time.Second,
	30 * time.Second,
	2 * time.Minute,
	10 * time.Minute,
}

type ForwardOutbox interface {
	Claim(time.Time, time.Duration) (*OutboxItem, error)
	Ack(*OutboxItem, time.Time) error
	Nack(
		*OutboxItem,
		error,
		time.Time,
		[]time.Duration,
	) (OutboxState, error)
}

type ForwardTransport interface {
	Send(context.Context, ForwardRequest) (ForwardACK, error)
}

type Forwarder struct {
	Outbox    ForwardOutbox
	Transport ForwardTransport
	Now       func() time.Time
	Lease     time.Duration
	Backoff   []time.Duration
}

func (forwarder Forwarder) ForwardOne(
	ctx context.Context,
) (bool, error) {
	if !forwarder.valid() {
		return false, ErrInvalidForwarder
	}
	if ctx == nil {
		return false, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	now := forwarder.now()
	if now.IsZero() {
		return false, ErrInvalidForwarder
	}
	item, err := forwarder.Outbox.Claim(now, forwarder.Lease)
	if err != nil {
		return false, ErrForwardPersistence
	}
	if item == nil {
		return false, nil
	}

	request, err := NewForwardRequest(item)
	if err != nil {
		return true, forwarder.nack(
			item,
			ErrInvalidForwardRequest,
			now,
			ErrInvalidForwardRequest,
		)
	}
	ack, err := forwarder.Transport.Send(ctx, request)
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidForwardACK):
			return true, forwarder.nack(
				item,
				ErrInvalidForwardACK,
				now,
				ErrInvalidForwardACK,
			)
		case errors.Is(err, ErrDuplicateACK):
			return true, forwarder.nack(
				item,
				ErrDuplicateACK,
				now,
				ErrDuplicateACK,
			)
		case errors.Is(err, io.EOF):
			return true, forwarder.nack(item, ErrForwardTransport, now, io.EOF)
		case errors.Is(err, context.Canceled):
			return true, forwarder.nack(item, context.Canceled, now, context.Canceled)
		case errors.Is(err, context.DeadlineExceeded):
			return true, forwarder.nack(
				item,
				context.DeadlineExceeded,
				now,
				context.DeadlineExceeded,
			)
		default:
			return true, forwarder.nack(
				item,
				ErrForwardTransport,
				now,
				ErrForwardTransport,
			)
		}
	}
	if err := ack.Validate(item); err != nil {
		return true, forwarder.nack(
			item,
			ErrInvalidForwardACK,
			now,
			ErrInvalidForwardACK,
		)
	}

	// Ack mutates its argument before publishing history. Use a copy so a
	// failed local transition leaves the original inflight claim available to
	// Outbox.Recover instead of making an unsafe Nack transition.
	acknowledged := *item
	if err := forwarder.Outbox.Ack(&acknowledged, forwarder.now()); err != nil {
		return true, ErrForwardPersistence
	}
	return true, nil
}

func (forwarder Forwarder) Run(ctx context.Context) error {
	if !forwarder.valid() {
		return ErrInvalidForwarder
	}
	for {
		if ctx == nil || ctx.Err() != nil {
			return nil
		}
		forwarded, err := forwarder.ForwardOne(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) ||
				errors.Is(err, context.Canceled) ||
				errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			return err
		}
		if !forwarded {
			return nil
		}
	}
}

func (forwarder Forwarder) nack(
	item *OutboxItem,
	persistedCause error,
	now time.Time,
	returned error,
) error {
	backoff := forwarder.Backoff
	if len(backoff) == 0 {
		backoff = defaultForwardBackoff
	}
	if _, err := forwarder.Outbox.Nack(
		item,
		persistedCause,
		now,
		append([]time.Duration(nil), backoff...),
	); err != nil {
		return ErrForwardPersistence
	}
	return returned
}

func (forwarder Forwarder) now() time.Time {
	if forwarder.Now != nil {
		return forwarder.Now().UTC()
	}
	return time.Now().UTC()
}

func (forwarder Forwarder) valid() bool {
	if forwarder.Outbox == nil ||
		forwarder.Transport == nil ||
		forwarder.Lease < 0 {
		return false
	}
	for _, delay := range forwarder.Backoff {
		if delay <= 0 {
			return false
		}
	}
	return true
}

var _ ForwardOutbox = (*Outbox)(nil)
var _ ForwardTransport = (*StdioTransport)(nil)
