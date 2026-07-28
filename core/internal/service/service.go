package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/liming0791/agentbell/core/internal/config"
	"github.com/liming0791/agentbell/core/internal/queue"
	"github.com/liming0791/agentbell/core/internal/render"
	m2settings "github.com/liming0791/agentbell/core/internal/settings"
)

var DefaultBackoff = []time.Duration{
	time.Second,
	5 * time.Second,
	30 * time.Second,
	2 * time.Minute,
	10 * time.Minute,
}

const (
	historyRetention = 30 * 24 * time.Hour
	deadRetention    = 90 * 24 * time.Hour
	maximumDeadItems = 1000
)

type Sender interface {
	Send(ctx context.Context, channel config.Channel, text string) error
}

// BackgroundWorker is an optional fail-safe service companion. A worker
// failure is isolated from the local notification queue.
type BackgroundWorker interface {
	Run(context.Context) error
}

type Service struct {
	Queue         *queue.Queue
	LoadConfig    func() (config.Config, error)
	Sender        Sender
	SenderFactory func(config.Config) Sender
	Processor     *Processor
	Now           func() time.Time
	Lease         time.Duration
	PollInterval  time.Duration
	Backoff       []time.Duration
	Workers       []BackgroundWorker
}

func (service *Service) ProcessOne(ctx context.Context) (bool, error) {
	if service.Queue == nil || service.LoadConfig == nil ||
		(service.Sender == nil && service.SenderFactory == nil) {
		return false, errors.New("service dependencies are incomplete")
	}
	now := service.now()
	item, err := service.Queue.Claim(now, service.lease())
	if err != nil || item == nil {
		return false, err
	}

	configValue, err := service.LoadConfig()
	if err != nil {
		return true, service.failItem(item, err, now)
	}

	if service.Processor == nil {
		if item.Ledger != nil {
			err = errors.New("M2 processor is required for an item with a delivery ledger")
			return true, service.failItem(item, err, now)
		}
		return true, service.processLegacy(ctx, item, configValue, now)
	}

	settingsValue, err := service.Processor.loadSettings()
	if errors.Is(err, m2settings.ErrNotFound) && item.Ledger == nil {
		return true, service.processLegacy(ctx, item, configValue, now)
	}
	if err != nil {
		return true, service.failItem(item, err, now)
	}
	return true, service.processM2(ctx, item, configValue, settingsValue, now)
}

func (service *Service) processLegacy(
	ctx context.Context,
	item *queue.Item,
	configValue config.Config,
	now time.Time,
) error {
	if !configValue.EventEnabled(item.Event.Event) {
		return service.Queue.Ack(item, now)
	}
	channel, ok := configValue.Default()
	if !ok {
		return service.failItem(item, errors.New("default channel is missing"), now)
	}

	text := render.Text(item.Event, configValue)
	sender := service.sender(configValue)
	if sender == nil {
		return service.failItem(
			item,
			errors.New("configured sender is unavailable"),
			now,
		)
	}
	if err := sender.Send(ctx, channel, text); err != nil {
		backoff := service.backoff()
		if isPermanent(err) {
			backoff = nil
		}
		_, nackErr := service.Queue.Nack(item, err, now, backoff)
		return errors.Join(err, nackErr)
	}
	return service.Queue.Ack(item, now)
}

func (service *Service) sender(configValue config.Config) Sender {
	sender := service.Sender
	if service.SenderFactory != nil {
		sender = service.SenderFactory(configValue)
	}
	return sender
}

func (service *Service) failItem(
	item *queue.Item,
	cause error,
	now time.Time,
) error {
	if item.Ledger != nil {
		return errors.Join(cause, service.failResolvedTargets(item, cause, now))
	}
	_, nackErr := service.Queue.Nack(item, cause, now, service.backoff())
	return errors.Join(cause, nackErr)
}

func (service *Service) failResolvedTargets(
	item *queue.Item,
	cause error,
	now time.Time,
) error {
	targets, err := service.Queue.ResolveTargets(item, nil, now)
	if err != nil {
		return err
	}
	var failures []error
	for _, target := range targets {
		if targetErr := service.failTarget(item, target, cause, now); targetErr != nil {
			failures = append(failures, targetErr)
		}
	}
	if finishErr := service.finishResolvedItem(item, now); finishErr != nil {
		failures = append(failures, finishErr)
	}
	return errors.Join(failures...)
}

func (service *Service) Run(ctx context.Context) error {
	lock, err := acquireLock(filepath.Join(service.Queue.Root(), "service.lock"), 15*time.Second)
	if err != nil {
		return err
	}
	defer lock.Release()

	if _, err := service.Queue.RecoverExpired(service.now()); err != nil {
		return err
	}
	now := service.now()
	if _, err := service.Queue.CleanupHistory(now.Add(-historyRetention)); err != nil {
		return err
	}
	if _, err := service.Queue.CleanupDead(
		now.Add(-deadRetention),
		maximumDeadItems,
	); err != nil {
		return err
	}
	heartbeatContext, cancelHeartbeat := context.WithCancel(ctx)
	defer cancelHeartbeat()
	go lock.Heartbeat(heartbeatContext, 5*time.Second)

	workerContext, cancelWorkers := context.WithCancel(ctx)
	var workerGroup sync.WaitGroup
	for _, worker := range service.Workers {
		if worker == nil {
			continue
		}
		workerGroup.Add(1)
		go func(value BackgroundWorker) {
			defer workerGroup.Done()
			if err := value.Run(workerContext); err != nil &&
				workerContext.Err() == nil {
				fmt.Fprintln(
					os.Stderr,
					"agentbell service: background worker unavailable",
				)
			}
		}(worker)
	}
	defer func() {
		cancelWorkers()
		workerGroup.Wait()
	}()

	ticker := time.NewTicker(service.pollInterval())
	defer ticker.Stop()
	for {
		processed, err := service.ProcessOne(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "agentbell service: %v\n", err)
		}
		if processed {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (service *Service) now() time.Time {
	if service.Now != nil {
		return service.Now().UTC()
	}
	return time.Now().UTC()
}

func (service *Service) lease() time.Duration {
	if service.Lease > 0 {
		return service.Lease
	}
	return 30 * time.Second
}

func (service *Service) pollInterval() time.Duration {
	if service.PollInterval > 0 {
		return service.PollInterval
	}
	return 500 * time.Millisecond
}

func (service *Service) backoff() []time.Duration {
	if len(service.Backoff) > 0 {
		return service.Backoff
	}
	return DefaultBackoff
}

func isPermanent(err error) bool {
	var value interface {
		Permanent() bool
	}
	return errors.As(err, &value) && value.Permanent()
}
