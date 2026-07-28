package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/liming0791/agentbell/core/internal/config"
	"github.com/liming0791/agentbell/core/internal/event"
	"github.com/liming0791/agentbell/core/internal/policy"
	"github.com/liming0791/agentbell/core/internal/queue"
	"github.com/liming0791/agentbell/core/internal/settings"
)

// Processor enables the M2 settings, policy, quiet-hours, and fan-out path.
// Leaving Service.Processor nil preserves the M1 delivery path.
type Processor struct {
	LoadSettings func() (settings.Settings, error)
	QuietOptions policy.QuietOptions
	SourceName   func(event.Notification) string
}

// ReceiptSender is optional. Existing Sender implementations remain valid;
// senders that can return a provider message identifier may persist it in the
// per-target delivery ledger.
type ReceiptSender interface {
	SendWithReceipt(
		ctx context.Context,
		channel config.Channel,
		text string,
	) (messageID string, err error)
}

func (processor *Processor) loadSettings() (settings.Settings, error) {
	if processor == nil || processor.LoadSettings == nil {
		return settings.Settings{}, errors.New("M2 settings loader is unavailable")
	}
	value, err := processor.LoadSettings()
	if err != nil {
		return settings.Settings{}, err
	}
	if err := value.Validate(); err != nil {
		return settings.Settings{}, fmt.Errorf("validate M2 settings: %w", err)
	}
	return value, nil
}

func (service *Service) processM2(
	ctx context.Context,
	item *queue.Item,
	configValue config.Config,
	settingsValue settings.Settings,
	now time.Time,
) error {
	var targets []queue.DeliveryTarget
	if item.Ledger == nil {
		decision, err := policy.Evaluate(
			settingsValue,
			item.Event,
			policy.Defaults{
				ChannelIDs: []string{configValue.DefaultChannel},
				TemplateID: settingsValue.DefaultTemplate,
			},
		)
		if err != nil {
			return service.failItem(item, err, now)
		}
		if !decision.Enabled {
			return service.Queue.Ack(item, now)
		}
		targets, err = resolveDecisionTargets(configValue, settingsValue, decision)
		if err != nil {
			return service.failItem(item, err, now)
		}
	}

	quietDecision, err := (policy.QuietEvaluator{
		Now: func() time.Time { return now },
	}).Evaluate(settingsValue.QuietHours, item.Event, service.Processor.QuietOptions)
	if err != nil {
		return service.failItem(item, err, now)
	}
	if quietDecision.Active && quietDecision.Action == policy.ActionDrop {
		return service.dropResolvedOrNewItem(item, now)
	}

	ready, err := service.Queue.ResolveTargets(item, targets, now)
	if err != nil {
		return err
	}
	if quietDecision.Active && quietDecision.Action == policy.ActionDefer {
		if deliveryTerminal(item.Ledger) {
			return service.Queue.Ack(item, now)
		}
		return service.Queue.Defer(item, now, quietDecision.DeferUntil)
	}
	if len(ready) == 0 {
		return service.finishResolvedItem(item, now)
	}

	sender := service.sender(configValue)
	if sender == nil {
		cause := errors.New("configured sender is unavailable")
		return errors.Join(cause, service.failResolvedTargets(item, cause, now))
	}
	input, err := policy.InputFromNotification(
		item.Event,
		service.Processor.sourceName(item.Event),
	)
	if err != nil {
		return errors.Join(err, service.failResolvedTargets(item, err, now))
	}

	var failures []error
	for _, target := range ready {
		channel, ok := channelByID(configValue, target.ChannelID)
		if !ok {
			cause := fmt.Errorf("delivery channel %q is missing", target.ChannelID)
			failures = append(failures, cause)
			if err := service.failTarget(item, target, cause, now); err != nil {
				failures = append(failures, err)
			}
			continue
		}
		template, ok := templateByID(settingsValue, target.TemplateID)
		if !ok {
			cause := fmt.Errorf("delivery template %q is missing", target.TemplateID)
			failures = append(failures, cause)
			if err := service.failTarget(item, target, cause, now); err != nil {
				failures = append(failures, err)
			}
			continue
		}
		text, err := policy.Render(template, input)
		if err != nil {
			failures = append(failures, err)
			if targetErr := service.failTarget(item, target, err, now); targetErr != nil {
				failures = append(failures, targetErr)
			}
			continue
		}
		messageID, err := sendM2(ctx, sender, channel, text)
		if err != nil {
			failures = append(failures, err)
			if targetErr := service.failTarget(item, target, err, now); targetErr != nil {
				failures = append(failures, targetErr)
			}
			continue
		}
		if err := service.Queue.AckTarget(item, target, messageID, now); err != nil {
			failures = append(failures, err)
		}
	}
	if err := service.finishResolvedItem(item, now); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}

func (service *Service) failTarget(
	item *queue.Item,
	target queue.DeliveryTarget,
	cause error,
	now time.Time,
) error {
	entry, ok := ledgerEntry(item.Ledger, target)
	if !ok {
		return fmt.Errorf(
			"delivery target %q/%q is absent from ledger",
			target.ChannelID,
			target.TemplateID,
		)
	}
	backoff := service.backoff()
	if isPermanent(cause) || entry.Attempts+1 >= len(backoff) {
		return service.Queue.DeadTarget(item, target, cause, now)
	}
	delay := backoff[entry.Attempts]
	if delay <= 0 {
		delay = time.Millisecond
	}
	return service.Queue.NackTarget(item, target, cause, now.Add(delay), now)
}

func (service *Service) finishResolvedItem(item *queue.Item, now time.Time) error {
	if deliveryTerminal(item.Ledger) {
		return service.Queue.Ack(item, now)
	}
	next := time.Time{}
	for _, entry := range item.Ledger {
		if entry.State != queue.DeliveryPending {
			continue
		}
		candidate := entry.NextAttemptAt
		if candidate.IsZero() || !candidate.After(now) {
			candidate = now.Add(service.pollInterval())
		}
		if next.IsZero() || candidate.Before(next) {
			next = candidate
		}
	}
	if next.IsZero() {
		return errors.New("non-terminal delivery ledger has no pending target")
	}
	return service.Queue.Defer(item, now, next)
}

func (service *Service) dropResolvedOrNewItem(item *queue.Item, now time.Time) error {
	if item.Ledger == nil {
		return service.Queue.Ack(item, now)
	}
	cause := errors.New("delivery suppressed by quiet-hours drop")
	for _, entry := range append([]queue.DeliveryLedgerEntry(nil), item.Ledger...) {
		if entry.State != queue.DeliveryPending {
			continue
		}
		target := queue.DeliveryTarget{
			ChannelID:  entry.ChannelID,
			TemplateID: entry.TemplateID,
		}
		if err := service.Queue.DeadTarget(item, target, cause, now); err != nil {
			return err
		}
	}
	return service.Queue.Ack(item, now)
}

func (processor *Processor) sourceName(notification event.Notification) string {
	if processor.SourceName != nil {
		return processor.SourceName(notification)
	}
	return notification.Source
}

func resolveDecisionTargets(
	configValue config.Config,
	settingsValue settings.Settings,
	decision policy.Decision,
) ([]queue.DeliveryTarget, error) {
	if _, ok := templateByID(settingsValue, decision.TemplateID); !ok {
		return nil, fmt.Errorf("policy template %q is missing", decision.TemplateID)
	}
	result := make([]queue.DeliveryTarget, 0, len(decision.ChannelIDs))
	for _, channelID := range decision.ChannelIDs {
		if _, ok := channelByID(configValue, channelID); !ok {
			return nil, fmt.Errorf("policy channel %q is missing", channelID)
		}
		result = append(result, queue.DeliveryTarget{
			ChannelID:  channelID,
			TemplateID: decision.TemplateID,
		})
	}
	return result, nil
}

func channelByID(configValue config.Config, id string) (config.Channel, bool) {
	for _, channel := range configValue.Channels {
		if channel.ID == id {
			return channel, true
		}
	}
	return config.Channel{}, false
}

func templateByID(
	settingsValue settings.Settings,
	id string,
) (settings.Template, bool) {
	for _, template := range settingsValue.Templates {
		if template.ID == id {
			return template, true
		}
	}
	return settings.Template{}, false
}

func ledgerEntry(
	ledger []queue.DeliveryLedgerEntry,
	target queue.DeliveryTarget,
) (queue.DeliveryLedgerEntry, bool) {
	for _, entry := range ledger {
		if entry.ChannelID == target.ChannelID && entry.TemplateID == target.TemplateID {
			return entry, true
		}
	}
	return queue.DeliveryLedgerEntry{}, false
}

func deliveryTerminal(ledger []queue.DeliveryLedgerEntry) bool {
	for _, entry := range ledger {
		if entry.State == queue.DeliveryPending {
			return false
		}
	}
	return len(ledger) > 0
}

func sendM2(
	ctx context.Context,
	sender Sender,
	channel config.Channel,
	text string,
) (string, error) {
	if receiptSender, ok := sender.(ReceiptSender); ok {
		return receiptSender.SendWithReceipt(ctx, channel, text)
	}
	return "", sender.Send(ctx, channel, text)
}
