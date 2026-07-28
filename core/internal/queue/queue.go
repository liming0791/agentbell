package queue

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/liming0791/agentbell/core/internal/event"
)

const QueueVersion = 1

var errRetryReservation = errors.New("retry idempotency reservation")

type State string

const (
	StatePending   State = "pending"
	StateInflight  State = "inflight"
	StateSucceeded State = "succeeded"
	StateDead      State = "dead"
)

var validStates = map[State]bool{
	StatePending: true, StateInflight: true, StateSucceeded: true, StateDead: true,
}

type Envelope struct {
	QueueVersion  int                   `json:"queueVersion"`
	ID            string                `json:"id"`
	State         State                 `json:"state"`
	Event         event.Notification    `json:"event"`
	Attempts      int                   `json:"attempts"`
	CreatedAt     time.Time             `json:"createdAt"`
	UpdatedAt     time.Time             `json:"updatedAt"`
	NextAttemptAt time.Time             `json:"nextAttemptAt,omitempty"`
	LeaseUntil    time.Time             `json:"leaseUntil,omitempty"`
	LastError     string                `json:"lastError,omitempty"`
	ManualRetries int                   `json:"manualRetries,omitempty"`
	LastRetriedAt time.Time             `json:"lastRetriedAt,omitempty"`
	Ledger        []DeliveryLedgerEntry `json:"ledger,omitempty"`
	Disposition   Disposition           `json:"disposition,omitempty"`
}

type Item struct {
	Envelope
	FileName string `json:"-"`
}

type Stats struct {
	Pending  int `json:"pending"`
	Inflight int `json:"inflight"`
	History  int `json:"history"`
	Dead     int `json:"dead"`
}

type Queue struct {
	root string
	mu   sync.Mutex
}

func Open(root string) (*Queue, error) {
	if root == "" {
		return nil, errors.New("queue root is required")
	}
	queue := &Queue{root: root}
	for _, directory := range []string{"pending", "inflight", "history", "dead", "tmp", "keys"} {
		if err := os.MkdirAll(queue.directory(directory), 0o700); err != nil {
			return nil, fmt.Errorf("create queue directory %s: %w", directory, err)
		}
	}
	return queue, nil
}

func (queue *Queue) Root() string {
	return queue.root
}

func (queue *Queue) Enqueue(notification event.Notification, now time.Time) (string, bool, error) {
	if err := notification.Validate(); err != nil {
		return "", false, err
	}

	queue.mu.Lock()
	defer queue.mu.Unlock()

	id, err := randomID()
	if err != nil {
		return "", false, err
	}
	keyName := idempotencyFileName(notification.IdempotencyKey)
	keyPath := filepath.Join(queue.directory("keys"), keyName)

reserve:
	keyFile, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		existingID, readErr := queue.waitForDurableReservation(keyPath)
		if errors.Is(readErr, errRetryReservation) {
			goto reserve
		}
		if readErr != nil {
			return "", false, readErr
		}
		return existingID, true, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("create idempotency marker: %w", err)
	}

	markerCommitted := false
	defer func() {
		_ = keyFile.Close()
		if !markerCommitted {
			_ = os.Remove(keyPath)
		}
	}()

	if _, err := io.WriteString(keyFile, id+"\n"); err != nil {
		return "", false, fmt.Errorf("write idempotency marker: %w", err)
	}
	if err := keyFile.Sync(); err != nil {
		return "", false, fmt.Errorf("sync idempotency marker: %w", err)
	}
	if err := keyFile.Close(); err != nil {
		return "", false, fmt.Errorf("close idempotency marker: %w", err)
	}

	envelope := Envelope{
		QueueVersion: QueueVersion,
		ID:           id,
		State:        StatePending,
		Event:        notification,
		CreatedAt:    now.UTC(),
		UpdatedAt:    now.UTC(),
	}
	fileName := fmt.Sprintf("%020d-%s.json", now.UTC().UnixNano(), id)
	if err := queue.writeNew("pending", fileName, envelope); err != nil {
		return "", false, err
	}

	markerCommitted = true
	return id, false, nil
}

func (queue *Queue) waitForDurableReservation(keyPath string) (string, error) {
	deadline := time.Now().Add(150 * time.Millisecond)
	for {
		value, err := os.ReadFile(keyPath)
		if errors.Is(err, os.ErrNotExist) {
			return "", errRetryReservation
		}
		if err != nil {
			return "", fmt.Errorf("read idempotency marker: %w", err)
		}
		id := strings.TrimSpace(string(value))
		if id != "" && queue.itemExists(id) {
			return id, nil
		}
		if time.Now().After(deadline) {
			info, statErr := os.Stat(keyPath)
			if errors.Is(statErr, os.ErrNotExist) {
				return "", errRetryReservation
			}
			if statErr != nil {
				return "", fmt.Errorf("stat idempotency marker: %w", statErr)
			}
			if time.Since(info.ModTime()) > time.Second {
				if removeErr := os.Remove(keyPath); removeErr != nil &&
					!errors.Is(removeErr, os.ErrNotExist) {
					return "", fmt.Errorf("remove stale idempotency marker: %w", removeErr)
				}
				return "", errRetryReservation
			}
			return "", errors.New("idempotency reservation is not durable yet")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (queue *Queue) itemExists(id string) bool {
	for _, directory := range []string{"pending", "inflight", "history", "dead"} {
		matches, err := filepath.Glob(
			filepath.Join(queue.directory(directory), "*-"+id+".json"),
		)
		if err == nil && len(matches) > 0 {
			return true
		}
	}
	return false
}

func (queue *Queue) Claim(now time.Time, lease time.Duration) (*Item, error) {
	queue.mu.Lock()
	defer queue.mu.Unlock()

	items, err := queue.listUnlocked(StatePending)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if !item.NextAttemptAt.IsZero() && item.NextAttemptAt.After(now) {
			continue
		}
		item.State = StateInflight
		item.UpdatedAt = now.UTC()
		item.LeaseUntil = now.UTC().Add(lease)
		sourcePath := filepath.Join(queue.directory("pending"), item.FileName)
		if err := queue.writeNew("inflight", item.FileName, item.Envelope); err != nil {
			if errors.Is(err, os.ErrExist) {
				_ = os.Remove(sourcePath)
				continue
			}
			return nil, err
		}
		if err := os.Remove(sourcePath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("finish queue claim: %w", err)
		}
		return &item, nil
	}
	return nil, nil
}

func (queue *Queue) Ack(item *Item, now time.Time) error {
	queue.mu.Lock()
	defer queue.mu.Unlock()

	if err := validateInflightItem(item); err != nil {
		return err
	}
	if item.Ledger != nil {
		if err := validateDeliveryEnvelope(item.Envelope); err != nil {
			return fmt.Errorf("invalid delivery ledger: %w", err)
		}
		if !deliveryLedgerTerminal(item.Ledger) {
			return errors.New("cannot acknowledge envelope before all targets are terminal")
		}
	}
	updated := item.Envelope
	updated.State = StateSucceeded
	updated.UpdatedAt = now.UTC()
	updated.LeaseUntil = time.Time{}
	updated.NextAttemptAt = time.Time{}
	updated.LastError = ""
	if err := queue.transitionEnvelope("inflight", "history", item.FileName, updated); err != nil {
		return err
	}
	item.Envelope = updated
	return nil
}

func (queue *Queue) Nack(item *Item, cause error, now time.Time, backoff []time.Duration) (State, error) {
	queue.mu.Lock()
	defer queue.mu.Unlock()

	if err := validateInflightItem(item); err != nil {
		return "", err
	}
	if item.Ledger != nil {
		return "", errors.New("envelope nack is not allowed after targets are resolved")
	}
	updated := item.Envelope
	updated.Attempts++
	updated.UpdatedAt = now.UTC()
	updated.LeaseUntil = time.Time{}
	updated.LastError = truncateError(cause)

	if updated.Attempts >= len(backoff) {
		updated.State = StateDead
		updated.NextAttemptAt = time.Time{}
		if err := queue.transitionEnvelope("inflight", "dead", item.FileName, updated); err != nil {
			return StateDead, err
		}
		item.Envelope = updated
		return StateDead, nil
	}

	updated.State = StatePending
	updated.NextAttemptAt = now.UTC().Add(backoff[updated.Attempts-1])
	if err := queue.transitionEnvelope("inflight", "pending", item.FileName, updated); err != nil {
		return StatePending, err
	}
	item.Envelope = updated
	return StatePending, nil
}

func (queue *Queue) RecoverExpired(now time.Time) (int, error) {
	queue.mu.Lock()
	defer queue.mu.Unlock()

	items, err := queue.listUnlocked(StateInflight)
	if err != nil {
		return 0, err
	}
	recovered := 0
	for index := range items {
		item := &items[index]
		completedTransition := false
		for _, directory := range []string{"pending", "history", "dead"} {
			destination := filepath.Join(queue.directory(directory), item.FileName)
			if _, statErr := os.Stat(destination); statErr == nil {
				completedTransition = true
				break
			}
		}
		if completedTransition {
			_ = os.Remove(filepath.Join(queue.directory("inflight"), item.FileName))
			continue
		}
		if !item.LeaseUntil.IsZero() && item.LeaseUntil.After(now) {
			continue
		}
		item.State = StatePending
		item.UpdatedAt = now.UTC()
		item.LeaseUntil = time.Time{}
		if err := queue.transition("inflight", "pending", item); err != nil {
			return recovered, err
		}
		recovered++
	}
	return recovered, nil
}

func (queue *Queue) List(state State) ([]Envelope, error) {
	queue.mu.Lock()
	defer queue.mu.Unlock()

	items, err := queue.listUnlocked(state)
	if err != nil {
		return nil, err
	}
	result := make([]Envelope, 0, len(items))
	for _, item := range items {
		result = append(result, item.Envelope)
	}
	return result, nil
}

func (queue *Queue) Retry(id string, now time.Time) error {
	queue.mu.Lock()
	defer queue.mu.Unlock()

	items, err := queue.listUnlocked(StateDead)
	if err != nil {
		return err
	}
	for index := range items {
		item := &items[index]
		if item.ID != id {
			continue
		}
		item.State = StatePending
		item.Attempts = 0
		item.LastError = ""
		item.NextAttemptAt = time.Time{}
		item.ManualRetries++
		item.LastRetriedAt = now.UTC()
		item.UpdatedAt = now.UTC()
		return queue.transition("dead", "pending", item)
	}
	return fmt.Errorf("dead event %q not found", id)
}

func (queue *Queue) Stats() (Stats, error) {
	queue.mu.Lock()
	defer queue.mu.Unlock()

	var result Stats
	for state, destination := range map[State]*int{
		StatePending:   &result.Pending,
		StateInflight:  &result.Inflight,
		StateSucceeded: &result.History,
		StateDead:      &result.Dead,
	} {
		items, err := queue.listUnlocked(state)
		if err != nil {
			return Stats{}, err
		}
		*destination = len(items)
	}
	return result, nil
}

func (queue *Queue) CleanupHistory(before time.Time) (int, error) {
	queue.mu.Lock()
	defer queue.mu.Unlock()

	items, err := queue.listUnlocked(StateSucceeded)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, item := range items {
		if !item.UpdatedAt.Before(before) {
			continue
		}
		if err := os.Remove(filepath.Join(queue.directory("history"), item.FileName)); err != nil {
			return removed, err
		}
		_ = os.Remove(filepath.Join(
			queue.directory("keys"),
			idempotencyFileName(item.Event.IdempotencyKey),
		))
		removed++
	}
	return removed, nil
}

func (queue *Queue) CleanupDead(before time.Time, maximum int) (int, error) {
	queue.mu.Lock()
	defer queue.mu.Unlock()

	items, err := queue.listUnlocked(StateDead)
	if err != nil {
		return 0, err
	}
	removed := 0
	excess := 0
	if maximum >= 0 && len(items) > maximum {
		excess = len(items) - maximum
	}
	for index, item := range items {
		if index >= excess && !item.UpdatedAt.Before(before) {
			continue
		}
		if err := os.Remove(filepath.Join(queue.directory("dead"), item.FileName)); err != nil {
			return removed, err
		}
		_ = os.Remove(filepath.Join(
			queue.directory("keys"),
			idempotencyFileName(item.Event.IdempotencyKey),
		))
		removed++
	}
	return removed, nil
}

func (queue *Queue) transition(source, destination string, item *Item) error {
	if err := queue.writeNew(destination, item.FileName, item.Envelope); err != nil {
		return err
	}
	sourcePath := filepath.Join(queue.directory(source), item.FileName)
	if err := os.Remove(sourcePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove queue source %s: %w", source, err)
	}
	return nil
}

func (queue *Queue) listUnlocked(state State) ([]Item, error) {
	if !validStates[state] {
		return nil, fmt.Errorf("unsupported queue state %q", state)
	}
	directory := queue.stateDirectory(state)
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	items := make([]Item, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		value, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			return nil, err
		}
		var envelope Envelope
		if err := json.Unmarshal(value, &envelope); err != nil {
			if quarantineErr := queue.quarantine(directory, entry.Name()); quarantineErr != nil {
				return nil, fmt.Errorf("invalid queue item and quarantine failed: %w", quarantineErr)
			}
			continue
		}
		if envelope.QueueVersion != QueueVersion ||
			envelope.ID == "" ||
			validateDeliveryEnvelope(envelope) != nil {
			if quarantineErr := queue.quarantine(directory, entry.Name()); quarantineErr != nil {
				return nil, quarantineErr
			}
			continue
		}
		items = append(items, Item{Envelope: envelope, FileName: entry.Name()})
	}
	sort.Slice(items, func(left, right int) bool {
		return items[left].CreatedAt.Before(items[right].CreatedAt)
	})
	return items, nil
}

func (queue *Queue) quarantine(sourceDirectory, fileName string) error {
	sourcePath := filepath.Join(sourceDirectory, fileName)
	destination := filepath.Join(queue.directory("dead"), fileName+".corrupt")
	if err := os.Rename(sourcePath, destination); err != nil {
		return fmt.Errorf("quarantine corrupt queue item: %w", err)
	}
	return nil
}

func (queue *Queue) writeNew(state, fileName string, envelope Envelope) error {
	destination := filepath.Join(queue.directory(state), fileName)
	if _, err := os.Stat(destination); err == nil {
		return os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return queue.write(destination, envelope)
}

func (queue *Queue) write(destination string, envelope Envelope) error {
	value, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return err
	}
	value = append(value, '\n')
	tempFile, err := os.CreateTemp(queue.directory("tmp"), "queue-*.tmp")
	if err != nil {
		return fmt.Errorf("create queue temp file: %w", err)
	}
	tempPath := tempFile.Name()
	defer os.Remove(tempPath)

	if err := tempFile.Chmod(0o600); err != nil {
		_ = tempFile.Close()
		return err
	}
	if _, err := tempFile.Write(value); err != nil {
		_ = tempFile.Close()
		return err
	}
	if err := tempFile.Sync(); err != nil {
		_ = tempFile.Close()
		return err
	}
	if err := tempFile.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, destination); err != nil {
		return fmt.Errorf("commit queue item: %w", err)
	}
	return nil
}

func (queue *Queue) stateDirectory(state State) string {
	if state == StateSucceeded {
		return queue.directory("history")
	}
	return queue.directory(string(state))
}

func (queue *Queue) directory(name string) string {
	return filepath.Join(queue.root, name)
}

func randomID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func idempotencyFileName(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:]) + ".key"
}

func truncateError(err error) string {
	if err == nil {
		return ""
	}
	value := err.Error()
	if len(value) > 512 {
		return value[:512]
	}
	return value
}
