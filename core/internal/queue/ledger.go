package queue

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const maximumDeliveryIdentifierLength = 256

type DeliveryState string

const (
	DeliveryPending   DeliveryState = "pending"
	DeliverySucceeded DeliveryState = "succeeded"
	DeliveryDead      DeliveryState = "dead"
)

type Disposition string

const (
	DispositionPending   Disposition = "pending"
	DispositionPartial   Disposition = "partial"
	DispositionSucceeded Disposition = "succeeded"
	DispositionDead      Disposition = "dead"
)

type DeliveryTarget struct {
	ChannelID  string `json:"channelId"`
	TemplateID string `json:"templateId"`
}

type DeliveryLedgerEntry struct {
	ChannelID     string        `json:"channelId"`
	TemplateID    string        `json:"templateId"`
	State         DeliveryState `json:"state"`
	Attempts      int           `json:"attempts"`
	NextAttemptAt time.Time     `json:"nextAttemptAt,omitempty"`
	LastError     string        `json:"lastError,omitempty"`
	MessageID     string        `json:"messageId,omitempty"`
}

type RollbackPreflight struct {
	HasPartialSuccess bool           `json:"hasPartialSuccess"`
	Items             []RollbackItem `json:"items,omitempty"`
}

type RollbackItem struct {
	ID               string `json:"id"`
	State            State  `json:"state"`
	SucceededTargets int    `json:"succeededTargets"`
	RemainingTargets int    `json:"remainingTargets"`
}

func (queue *Queue) ResolveTargets(
	item *Item,
	targets []DeliveryTarget,
	now time.Time,
) ([]DeliveryTarget, error) {
	queue.mu.Lock()
	defer queue.mu.Unlock()

	if err := validateInflightItem(item); err != nil {
		return nil, err
	}
	if item.Ledger == nil {
		ledger, err := newDeliveryLedger(targets)
		if err != nil {
			return nil, err
		}
		updated := item.Envelope
		updated.Ledger = ledger
		updated.Disposition = dispositionFor(ledger)
		updated.UpdatedAt = now.UTC()
		if err := validateDeliveryEnvelope(updated); err != nil {
			return nil, err
		}
		if err := queue.replace("inflight", item.FileName, updated); err != nil {
			return nil, err
		}
		item.Envelope = updated
	} else if err := validateDeliveryEnvelope(item.Envelope); err != nil {
		return nil, fmt.Errorf("invalid delivery ledger: %w", err)
	}
	return readyTargets(item.Ledger, now), nil
}

func (queue *Queue) AckTarget(
	item *Item,
	target DeliveryTarget,
	messageID string,
	now time.Time,
) error {
	queue.mu.Lock()
	defer queue.mu.Unlock()

	return queue.updateTarget(item, target, func(entry *DeliveryLedgerEntry) error {
		switch entry.State {
		case DeliverySucceeded:
			return nil
		case DeliveryPending:
			entry.State = DeliverySucceeded
			entry.Attempts++
			entry.NextAttemptAt = time.Time{}
			entry.LastError = ""
			entry.MessageID = messageID
			return nil
		default:
			return fmt.Errorf("cannot acknowledge target in %q state", entry.State)
		}
	}, now)
}

func (queue *Queue) NackTarget(
	item *Item,
	target DeliveryTarget,
	cause error,
	nextAttemptAt time.Time,
	now time.Time,
) error {
	queue.mu.Lock()
	defer queue.mu.Unlock()

	if cause == nil {
		return errors.New("target nack cause is required")
	}
	return queue.updateTarget(item, target, func(entry *DeliveryLedgerEntry) error {
		if entry.State != DeliveryPending {
			return fmt.Errorf("cannot nack target in %q state", entry.State)
		}
		entry.Attempts++
		entry.NextAttemptAt = nextAttemptAt.UTC()
		entry.LastError = truncateError(cause)
		entry.MessageID = ""
		return nil
	}, now)
}

func (queue *Queue) DeadTarget(
	item *Item,
	target DeliveryTarget,
	cause error,
	now time.Time,
) error {
	queue.mu.Lock()
	defer queue.mu.Unlock()

	if cause == nil {
		return errors.New("target dead-letter cause is required")
	}
	return queue.updateTarget(item, target, func(entry *DeliveryLedgerEntry) error {
		switch entry.State {
		case DeliveryDead:
			return nil
		case DeliveryPending:
			entry.State = DeliveryDead
			entry.Attempts++
			entry.NextAttemptAt = time.Time{}
			entry.LastError = truncateError(cause)
			entry.MessageID = ""
			return nil
		default:
			return fmt.Errorf("cannot dead-letter target in %q state", entry.State)
		}
	}, now)
}

func (queue *Queue) Defer(item *Item, now, nextAttemptAt time.Time) error {
	queue.mu.Lock()
	defer queue.mu.Unlock()

	if err := validateInflightItem(item); err != nil {
		return err
	}
	if item.Ledger == nil {
		return errors.New("cannot defer item before targets are resolved")
	}
	if nextAttemptAt.IsZero() || !nextAttemptAt.After(now) {
		return errors.New("defer time must be after now")
	}
	if err := validateDeliveryEnvelope(item.Envelope); err != nil {
		return fmt.Errorf("invalid delivery ledger: %w", err)
	}
	if deliveryLedgerTerminal(item.Ledger) {
		return errors.New("cannot defer a terminal delivery ledger")
	}

	updated := item.Envelope
	updated.State = StatePending
	updated.UpdatedAt = now.UTC()
	updated.LeaseUntil = time.Time{}
	updated.NextAttemptAt = nextAttemptAt.UTC()
	if err := queue.transitionEnvelope("inflight", "pending", item.FileName, updated); err != nil {
		return err
	}
	item.Envelope = updated
	return nil
}

func (queue *Queue) PreflightRollback() (RollbackPreflight, error) {
	queue.mu.Lock()
	defer queue.mu.Unlock()

	result := RollbackPreflight{}
	seen := map[string]bool{}
	for _, state := range []State{StatePending, StateInflight, StateSucceeded, StateDead} {
		items, err := queue.listUnlocked(state)
		if err != nil {
			return RollbackPreflight{}, err
		}
		for _, item := range items {
			if seen[item.ID] || item.Ledger == nil {
				continue
			}
			seen[item.ID] = true
			succeeded := 0
			for _, entry := range item.Ledger {
				if entry.State == DeliverySucceeded {
					succeeded++
				}
			}
			if succeeded == 0 || succeeded == len(item.Ledger) {
				continue
			}
			result.Items = append(result.Items, RollbackItem{
				ID:               item.ID,
				State:            item.State,
				SucceededTargets: succeeded,
				RemainingTargets: len(item.Ledger) - succeeded,
			})
		}
	}
	sort.Slice(result.Items, func(left, right int) bool {
		return result.Items[left].ID < result.Items[right].ID
	})
	result.HasPartialSuccess = len(result.Items) > 0
	return result, nil
}

func (queue *Queue) updateTarget(
	item *Item,
	target DeliveryTarget,
	update func(*DeliveryLedgerEntry) error,
	now time.Time,
) error {
	if err := validateInflightItem(item); err != nil {
		return err
	}
	if item.Ledger == nil {
		return errors.New("delivery targets are not resolved")
	}
	if err := validateDeliveryEnvelope(item.Envelope); err != nil {
		return fmt.Errorf("invalid delivery ledger: %w", err)
	}
	index := deliveryTargetIndex(item.Ledger, target)
	if index < 0 {
		return fmt.Errorf(
			"delivery target %q/%q not found",
			target.ChannelID,
			target.TemplateID,
		)
	}

	updated := item.Envelope
	updated.Ledger = append([]DeliveryLedgerEntry(nil), item.Ledger...)
	if err := update(&updated.Ledger[index]); err != nil {
		return err
	}
	updated.Disposition = dispositionFor(updated.Ledger)
	updated.UpdatedAt = now.UTC()
	if err := validateDeliveryEnvelope(updated); err != nil {
		return fmt.Errorf("invalid delivery ledger update: %w", err)
	}
	if err := queue.replace("inflight", item.FileName, updated); err != nil {
		return err
	}
	item.Envelope = updated
	return nil
}

func newDeliveryLedger(targets []DeliveryTarget) ([]DeliveryLedgerEntry, error) {
	if len(targets) == 0 {
		return nil, errors.New("at least one delivery target is required")
	}
	ledger := make([]DeliveryLedgerEntry, 0, len(targets))
	seen := map[string]bool{}
	for _, target := range targets {
		if err := validateDeliveryTarget(target); err != nil {
			return nil, err
		}
		key := deliveryTargetKey(target.ChannelID, target.TemplateID)
		if seen[key] {
			return nil, fmt.Errorf(
				"duplicate delivery target %q/%q",
				target.ChannelID,
				target.TemplateID,
			)
		}
		seen[key] = true
		ledger = append(ledger, DeliveryLedgerEntry{
			ChannelID:  target.ChannelID,
			TemplateID: target.TemplateID,
			State:      DeliveryPending,
		})
	}
	return ledger, nil
}

func validateDeliveryEnvelope(envelope Envelope) error {
	if envelope.Ledger == nil {
		if envelope.Disposition != "" {
			return errors.New("legacy item cannot have a disposition without a ledger")
		}
		return nil
	}
	if len(envelope.Ledger) == 0 {
		return errors.New("delivery ledger cannot be empty")
	}
	seen := map[string]bool{}
	for index, entry := range envelope.Ledger {
		target := DeliveryTarget{ChannelID: entry.ChannelID, TemplateID: entry.TemplateID}
		if err := validateDeliveryTarget(target); err != nil {
			return fmt.Errorf("ledger entry %d: %w", index, err)
		}
		key := deliveryTargetKey(entry.ChannelID, entry.TemplateID)
		if seen[key] {
			return fmt.Errorf(
				"duplicate delivery target %q/%q",
				entry.ChannelID,
				entry.TemplateID,
			)
		}
		seen[key] = true
		if entry.Attempts < 0 {
			return fmt.Errorf("ledger entry %d has negative attempts", index)
		}
		switch entry.State {
		case DeliveryPending:
			if entry.MessageID != "" {
				return fmt.Errorf("pending ledger entry %d has a message id", index)
			}
			if entry.Attempts == 0 && entry.LastError != "" {
				return fmt.Errorf("unattempted ledger entry %d has an error", index)
			}
		case DeliverySucceeded:
			if entry.Attempts < 1 {
				return fmt.Errorf("succeeded ledger entry %d has no attempt", index)
			}
			if !entry.NextAttemptAt.IsZero() || entry.LastError != "" {
				return fmt.Errorf("succeeded ledger entry %d has retry state", index)
			}
		case DeliveryDead:
			if entry.Attempts < 1 || entry.LastError == "" {
				return fmt.Errorf("dead ledger entry %d lacks failure detail", index)
			}
			if !entry.NextAttemptAt.IsZero() || entry.MessageID != "" {
				return fmt.Errorf("dead ledger entry %d has non-terminal state", index)
			}
		default:
			return fmt.Errorf("ledger entry %d has invalid state %q", index, entry.State)
		}
	}
	expected := dispositionFor(envelope.Ledger)
	if envelope.Disposition != expected {
		return fmt.Errorf(
			"delivery disposition %q does not match ledger %q",
			envelope.Disposition,
			expected,
		)
	}
	return nil
}

func validateDeliveryTarget(target DeliveryTarget) error {
	if target.ChannelID == "" || target.TemplateID == "" {
		return errors.New("delivery channelId and templateId are required")
	}
	if len(target.ChannelID) > maximumDeliveryIdentifierLength ||
		len(target.TemplateID) > maximumDeliveryIdentifierLength {
		return errors.New("delivery channelId or templateId is too long")
	}
	return nil
}

func validateInflightItem(item *Item) error {
	if item == nil {
		return errors.New("queue item is required")
	}
	if item.State != StateInflight {
		return fmt.Errorf("queue item must be inflight, got %q", item.State)
	}
	if item.FileName == "" {
		return errors.New("queue item filename is required")
	}
	return nil
}

func dispositionFor(ledger []DeliveryLedgerEntry) Disposition {
	succeeded := 0
	dead := 0
	for _, entry := range ledger {
		switch entry.State {
		case DeliverySucceeded:
			succeeded++
		case DeliveryDead:
			dead++
		}
	}
	switch {
	case succeeded == len(ledger):
		return DispositionSucceeded
	case dead == len(ledger):
		return DispositionDead
	case succeeded > 0:
		return DispositionPartial
	default:
		return DispositionPending
	}
}

func deliveryLedgerTerminal(ledger []DeliveryLedgerEntry) bool {
	for _, entry := range ledger {
		if entry.State == DeliveryPending {
			return false
		}
	}
	return true
}

func readyTargets(ledger []DeliveryLedgerEntry, now time.Time) []DeliveryTarget {
	result := make([]DeliveryTarget, 0, len(ledger))
	for _, entry := range ledger {
		if entry.State != DeliveryPending {
			continue
		}
		if !entry.NextAttemptAt.IsZero() && entry.NextAttemptAt.After(now) {
			continue
		}
		result = append(result, DeliveryTarget{
			ChannelID:  entry.ChannelID,
			TemplateID: entry.TemplateID,
		})
	}
	return result
}

func deliveryTargetIndex(ledger []DeliveryLedgerEntry, target DeliveryTarget) int {
	for index, entry := range ledger {
		if entry.ChannelID == target.ChannelID && entry.TemplateID == target.TemplateID {
			return index
		}
	}
	return -1
}

func deliveryTargetKey(channelID, templateID string) string {
	return channelID + "\x00" + templateID
}

func (queue *Queue) replace(directory, fileName string, envelope Envelope) error {
	path := filepath.Join(queue.directory(directory), fileName)
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("replace queue item: %w", err)
	}
	return queue.write(path, envelope)
}

func (queue *Queue) transitionEnvelope(
	source,
	destination,
	fileName string,
	envelope Envelope,
) error {
	if err := queue.writeNew(destination, fileName, envelope); err != nil {
		return err
	}
	sourcePath := filepath.Join(queue.directory(source), fileName)
	if err := os.Remove(sourcePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove queue source %s: %w", source, err)
	}
	return nil
}
