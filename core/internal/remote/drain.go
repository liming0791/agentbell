package remote

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/liming0791/agentbell/core/internal/relay"
)

const (
	defaultDrainLease = time.Minute
	defaultMaxItems   = 4096
)

var (
	ErrInvalidDrain = errors.New("remote outbox drain dependencies are incomplete")
	ErrDrainFailed  = errors.New("remote outbox drain failed")
	ErrDrainLimit   = errors.New("remote outbox drain item limit reached")
)

type DrainOptions struct {
	Now      func() time.Time
	Lease    time.Duration
	Backoff  []time.Duration
	MaxItems int
}

// DrainOutbox is the remote side of every stdio connector. It owns and closes
// reader and writer. Failed or cancelled exchanges are NACKed by Forwarder so
// the durable outbox retains its retry schedule.
func DrainOutbox(
	ctx context.Context,
	outbox *relay.Outbox,
	reader io.ReadCloser,
	writer io.WriteCloser,
	options DrainOptions,
) (int, error) {
	if ctx == nil {
		return 0, context.Canceled
	}
	if outbox == nil ||
		reader == nil ||
		writer == nil ||
		options.Lease < 0 ||
		options.MaxItems < 0 {
		return 0, ErrInvalidDrain
	}
	for _, delay := range options.Backoff {
		if delay <= 0 {
			return 0, ErrInvalidDrain
		}
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	transport, err := relay.NewStdioTransport(reader, writer)
	if err != nil {
		return 0, ErrInvalidDrain
	}
	defer transport.Close()
	lease := options.Lease
	if lease == 0 {
		lease = defaultDrainLease
	}
	maxItems := options.MaxItems
	if maxItems == 0 {
		maxItems = defaultMaxItems
	}
	forwarder := relay.Forwarder{
		Outbox:    outbox,
		Transport: transport,
		Now:       options.Now,
		Lease:     lease,
		Backoff:   append([]time.Duration(nil), options.Backoff...),
	}
	for count := 0; ; count++ {
		if count >= maxItems {
			return count, ErrDrainLimit
		}
		forwarded, err := forwarder.ForwardOne(ctx)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return count + boolCount(forwarded), ctxErr
			}
			return count + boolCount(forwarded), ErrDrainFailed
		}
		if !forwarded {
			return count, nil
		}
	}
}

func boolCount(value bool) int {
	if value {
		return 1
	}
	return 0
}
