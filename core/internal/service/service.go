package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/liming0791/agentbell/core/internal/config"
	"github.com/liming0791/agentbell/core/internal/queue"
	"github.com/liming0791/agentbell/core/internal/render"
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

type Service struct {
	Queue        *queue.Queue
	LoadConfig   func() (config.Config, error)
	Sender       Sender
	Now          func() time.Time
	Lease        time.Duration
	PollInterval time.Duration
	Backoff      []time.Duration
}

func (service *Service) ProcessOne(ctx context.Context) (bool, error) {
	if service.Queue == nil || service.LoadConfig == nil || service.Sender == nil {
		return false, errors.New("service dependencies are incomplete")
	}
	now := service.now()
	item, err := service.Queue.Claim(now, service.lease())
	if err != nil || item == nil {
		return false, err
	}

	settings, err := service.LoadConfig()
	if err != nil {
		_, nackErr := service.Queue.Nack(item, err, now, service.backoff())
		return true, errors.Join(err, nackErr)
	}
	if !settings.EventEnabled(item.Event.Event) {
		return true, service.Queue.Ack(item, now)
	}
	channel, ok := settings.Default()
	if !ok {
		err = errors.New("default channel is missing")
		_, nackErr := service.Queue.Nack(item, err, now, service.backoff())
		return true, errors.Join(err, nackErr)
	}

	text := render.Text(item.Event, settings)
	if err := service.Sender.Send(ctx, channel, text); err != nil {
		backoff := service.backoff()
		if isPermanent(err) {
			backoff = nil
		}
		_, nackErr := service.Queue.Nack(item, err, now, backoff)
		return true, errors.Join(err, nackErr)
	}
	return true, service.Queue.Ack(item, now)
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
