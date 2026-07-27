package remote

import (
	"context"
	"errors"
	"time"

	"github.com/liming0791/agentbell/core/internal/remoteconfig"
)

var (
	ErrInvalidRetryPolicy        = errors.New("invalid remote connector retry policy")
	ErrConnectorRetriesExhausted = errors.New("remote connector retries exhausted")
)

var defaultConnectorBackoff = []time.Duration{
	time.Second,
	5 * time.Second,
	30 * time.Second,
	2 * time.Minute,
	10 * time.Minute,
}

type RetryOptions struct {
	Backoff     []time.Duration
	MaxAttempts int
	Wait        func(context.Context, time.Duration) error
}

// Run retries transient process or protocol failures with context-aware
// backoff. Configuration and capability failures are permanent and are never
// retried. A successful remote drain ends the run.
func (puller Puller) Run(
	ctx context.Context,
	config remoteconfig.RemoteConfig,
	options RetryOptions,
) (int, error) {
	if ctx == nil {
		return 0, context.Canceled
	}
	if options.MaxAttempts < 0 {
		return 0, ErrInvalidRetryPolicy
	}
	backoff := options.Backoff
	if len(backoff) == 0 {
		backoff = defaultConnectorBackoff
	}
	for _, delay := range backoff {
		if delay <= 0 {
			return 0, ErrInvalidRetryPolicy
		}
	}
	attempts := options.MaxAttempts
	if attempts == 0 {
		attempts = len(backoff) + 1
	}
	wait := options.Wait
	if wait == nil {
		wait = waitContext
	}
	total := 0
	for attempt := 0; attempt < attempts; attempt++ {
		count, err := puller.Pull(ctx, config)
		total += count
		if err == nil {
			return total, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return total, ctxErr
		}
		if permanentConnectorError(err) {
			return total, err
		}
		if attempt+1 >= attempts {
			return total, ErrConnectorRetriesExhausted
		}
		delayIndex := attempt
		if delayIndex >= len(backoff) {
			delayIndex = len(backoff) - 1
		}
		if err := wait(ctx, backoff[delayIndex]); err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return total, ctxErr
			}
			if errors.Is(err, context.Canceled) ||
				errors.Is(err, context.DeadlineExceeded) {
				return total, err
			}
			return total, ErrConnectorRetriesExhausted
		}
	}
	return total, ErrConnectorRetriesExhausted
}

func permanentConnectorError(err error) bool {
	return errors.Is(err, ErrInvalidRemoteConfig) ||
		errors.Is(err, ErrNotPullConnector) ||
		errors.Is(err, ErrVendorCloudUnsupported) ||
		errors.Is(err, ErrInvalidPuller)
}

func waitContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
