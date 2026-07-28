package relay

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/liming0791/agentbell/core/internal/event"
)

const (
	outboxVersion          = 1
	defaultOutboxLease     = 30 * time.Second
	maxPersistedErrorBytes = 4096
)

type OutboxState string

const (
	OutboxPending  OutboxState = "pending"
	OutboxInflight OutboxState = "inflight"
	OutboxHistory  OutboxState = "history"
	OutboxDead     OutboxState = "dead"
)

var (
	ErrOutboxConflict = errors.New("relay outbox body conflict")
	ErrOutboxCapacity = errors.New("relay outbox capacity exceeded")
	outboxDirectories = []string{"pending", "inflight", "history", "dead", "tmp"}
	validOutboxStates = map[OutboxState]bool{
		OutboxPending:  true,
		OutboxInflight: true,
		OutboxHistory:  true,
		OutboxDead:     true,
	}
)

// EnqueueBounded serializes capacity checks across producer processes. History
// is excluded because acknowledged items no longer consume retry capacity.
// Duplicate deliveries remain idempotent even when the current limit is lower
// than the already persisted item.
func (outbox *Outbox) EnqueueBounded(
	exactBody []byte,
	signature SignatureMetadata,
	now time.Time,
	maxBytes int64,
) (string, bool, error) {
	if outbox == nil || outbox.root == "" || maxBytes <= 0 {
		return "", false, ErrOutboxCapacity
	}
	envelope, err := Decode(exactBody)
	if err != nil {
		return "", false, err
	}
	if envelope.Event.PrivacyLevel != event.PrivacyMetadataOnly {
		return "", false, errors.New(
			"relay outbox only accepts metadata-only events",
		)
	}
	if now.IsZero() {
		return "", false, errors.New("outbox enqueue time is required")
	}
	if err := signature.Validate(envelope, exactBody); err != nil {
		return "", false, err
	}
	id := receiptID(envelope.TeamID, envelope.Origin.ID, envelope.Delivery.Key)
	release, err := acquireStorageLock(filepath.Join(
		outbox.root,
		"tmp",
		"capacity.lock",
	))
	if err != nil {
		return "", false, err
	}
	defer release()
	for _, state := range []OutboxState{
		OutboxPending,
		OutboxInflight,
		OutboxHistory,
		OutboxDead,
	} {
		existing, readErr := outbox.read(state, id)
		if readErr == nil {
			if !bytes.Equal(existing.ExactBody, exactBody) {
				return "", true, ErrOutboxConflict
			}
			return existing.ID, true, nil
		}
		if !errors.Is(readErr, os.ErrNotExist) {
			return "", false, readErr
		}
	}
	now = now.UTC()
	prospective := OutboxItem{
		Version:     outboxVersion,
		ID:          id,
		State:       OutboxPending,
		DeliveryKey: envelope.Delivery.Key,
		BodyDigest:  bodyDigest(exactBody),
		ExactBody:   bytes.Clone(exactBody),
		Signature:   cloneSignature(signature),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	encoded, err := json.MarshalIndent(prospective, "", "  ")
	if err != nil {
		return "", false, err
	}
	usage, err := outbox.retryUsage()
	if err != nil {
		return "", false, err
	}
	if int64(len(encoded)+1) > maxBytes-usage {
		return "", false, ErrOutboxCapacity
	}
	return outbox.Enqueue(exactBody, signature, now)
}

func (outbox *Outbox) retryUsage() (int64, error) {
	var total int64
	for _, state := range []OutboxState{
		OutboxPending,
		OutboxInflight,
		OutboxDead,
	} {
		entries, err := os.ReadDir(filepath.Join(outbox.root, string(state)))
		if err != nil {
			return 0, err
		}
		for _, entry := range entries {
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				return 0, err
			}
			if info.Size() > maxBytesInt64-total {
				return 0, ErrOutboxCapacity
			}
			total += info.Size()
		}
	}
	return total, nil
}

const maxBytesInt64 = int64(^uint64(0) >> 1)

type SignatureMetadata struct {
	KeyID     string    `json:"keyId"`
	Method    string    `json:"method"`
	Target    string    `json:"target"`
	SentAt    time.Time `json:"sentAt"`
	Nonce     string    `json:"nonce"`
	Signature []byte    `json:"signature"`
}

type OutboxItem struct {
	Version       int               `json:"version"`
	ID            string            `json:"id"`
	State         OutboxState       `json:"state"`
	DeliveryKey   string            `json:"deliveryKey"`
	BodyDigest    string            `json:"bodyDigest"`
	ExactBody     []byte            `json:"exactBody"`
	Signature     SignatureMetadata `json:"signatureMetadata"`
	Attempts      int               `json:"attempts"`
	CreatedAt     time.Time         `json:"createdAt"`
	UpdatedAt     time.Time         `json:"updatedAt"`
	NextAttemptAt time.Time         `json:"nextAttemptAt,omitempty"`
	LeaseUntil    time.Time         `json:"leaseUntil,omitempty"`
	LastError     string            `json:"lastError,omitempty"`
}

type Outbox struct {
	root string
}

type OutboxStats struct {
	Pending  int `json:"pending"`
	Inflight int `json:"inflight"`
	History  int `json:"history"`
	Dead     int `json:"dead"`
	Retrying int `json:"retrying"`
	Total    int `json:"total"`
}

func OpenOutbox(root string) (*Outbox, error) {
	if root == "" {
		return nil, errors.New("outbox root is required")
	}
	if err := ensurePrivateDirectory(root); err != nil {
		return nil, fmt.Errorf("create relay outbox: %w", err)
	}
	for _, directory := range outboxDirectories {
		if err := ensurePrivateDirectory(filepath.Join(root, directory)); err != nil {
			return nil, fmt.Errorf("create relay outbox directory %s: %w", directory, err)
		}
	}
	return &Outbox{root: root}, nil
}

func (outbox *Outbox) Status(id string) (OutboxState, error) {
	if outbox == nil || outbox.root == "" || !validOutboxID(id) {
		return "", errors.New("invalid relay outbox item id")
	}
	for _, state := range []OutboxState{
		OutboxHistory,
		OutboxDead,
		OutboxInflight,
		OutboxPending,
	} {
		if _, err := outbox.read(state, id); err == nil {
			return state, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	return "", os.ErrNotExist
}

// LookupDelivery returns an already durable producer delivery without exposing
// its signed body. Remote producers use this before creating a fresh
// nonce/timestamp so a retried source Hook reuses the original exact envelope.
// Enqueue and EnqueueBounded intentionally retain their stricter exact-body
// conflict behavior for relay transport callers.
func (outbox *Outbox) LookupDelivery(
	teamID string,
	originID string,
	deliveryKey string,
) (string, OutboxState, bool, error) {
	if outbox == nil || outbox.root == "" {
		return "", "", false, errors.New("invalid relay outbox")
	}
	if err := validateIdentifier("teamId", teamID); err != nil {
		return "", "", false, err
	}
	if err := validateIdentifier("origin.id", originID); err != nil {
		return "", "", false, err
	}
	if !deliveryPattern.MatchString(deliveryKey) {
		return "", "", false, errors.New("invalid relay delivery key")
	}
	id := receiptID(teamID, originID, deliveryKey)
	for _, state := range []OutboxState{
		OutboxHistory,
		OutboxDead,
		OutboxInflight,
		OutboxPending,
	} {
		item, err := outbox.read(state, id)
		if err == nil {
			if item.DeliveryKey != deliveryKey {
				return "", "", false, ErrOutboxConflict
			}
			return id, state, true, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", "", false, err
		}
	}
	return id, "", false, nil
}

func (outbox *Outbox) Stats() (OutboxStats, error) {
	if outbox == nil || outbox.root == "" {
		return OutboxStats{}, errors.New("invalid relay outbox")
	}
	counts := make(map[OutboxState]int, len(validOutboxStates))
	for _, state := range []OutboxState{
		OutboxPending,
		OutboxInflight,
		OutboxHistory,
		OutboxDead,
	} {
		items, err := outbox.list(state)
		if err != nil {
			return OutboxStats{}, err
		}
		counts[state] = len(items)
	}
	return OutboxStats{
		Pending:  counts[OutboxPending],
		Inflight: counts[OutboxInflight],
		History:  counts[OutboxHistory],
		Dead:     counts[OutboxDead],
		Retrying: counts[OutboxPending] + counts[OutboxInflight],
		Total: counts[OutboxPending] +
			counts[OutboxInflight] +
			counts[OutboxHistory] +
			counts[OutboxDead],
	}, nil
}

func (outbox *Outbox) Enqueue(
	exactBody []byte,
	signature SignatureMetadata,
	now time.Time,
) (string, bool, error) {
	envelope, err := Decode(exactBody)
	if err != nil {
		return "", false, err
	}
	if envelope.Event.PrivacyLevel != event.PrivacyMetadataOnly {
		return "", false, errors.New("relay outbox only accepts metadata-only events")
	}
	if now.IsZero() {
		return "", false, errors.New("outbox enqueue time is required")
	}
	if err := signature.Validate(envelope, exactBody); err != nil {
		return "", false, err
	}
	id := receiptID(envelope.TeamID, envelope.Origin.ID, envelope.Delivery.Key)
	release, err := acquireStorageLock(filepath.Join(outbox.root, "tmp", id+".lock"))
	if err != nil {
		return "", false, err
	}
	defer release()

	for _, state := range []OutboxState{
		OutboxPending,
		OutboxInflight,
		OutboxHistory,
		OutboxDead,
	} {
		existing, readErr := outbox.read(state, id)
		if readErr == nil {
			if !bytes.Equal(existing.ExactBody, exactBody) {
				return "", true, ErrOutboxConflict
			}
			return existing.ID, true, nil
		}
		if !errors.Is(readErr, os.ErrNotExist) {
			return "", false, readErr
		}
	}

	now = now.UTC()
	item := &OutboxItem{
		Version:     outboxVersion,
		ID:          id,
		State:       OutboxPending,
		DeliveryKey: envelope.Delivery.Key,
		BodyDigest:  bodyDigest(exactBody),
		ExactBody:   bytes.Clone(exactBody),
		Signature:   cloneSignature(signature),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := outbox.writeNew(OutboxPending, item); err != nil {
		if errors.Is(err, os.ErrExist) {
			return id, true, nil
		}
		return "", false, err
	}
	return id, false, nil
}

func (outbox *Outbox) Claim(now time.Time, lease time.Duration) (*OutboxItem, error) {
	if now.IsZero() {
		return nil, errors.New("outbox claim time is required")
	}
	if lease == 0 {
		lease = defaultOutboxLease
	}
	items, err := outbox.list(OutboxPending)
	if err != nil {
		return nil, err
	}
	now = now.UTC()
	for index := range items {
		item := &items[index]
		if !item.NextAttemptAt.IsZero() && item.NextAttemptAt.After(now) {
			continue
		}
		item.State = OutboxInflight
		item.UpdatedAt = now
		item.LeaseUntil = now.Add(lease)
		if err := outbox.writeNew(OutboxInflight, item); err != nil {
			if errors.Is(err, os.ErrExist) {
				_ = removePrivateFile(outbox.path(OutboxPending, item.ID))
				continue
			}
			return nil, err
		}
		if err := removePrivateFile(outbox.path(OutboxPending, item.ID)); err != nil {
			return nil, fmt.Errorf("finish relay outbox claim: %w", err)
		}
		return item, nil
	}
	return nil, nil
}

func (outbox *Outbox) Ack(item *OutboxItem, now time.Time) error {
	if item == nil || item.State != OutboxInflight {
		return errors.New("only an inflight outbox item can be acknowledged")
	}
	if now.IsZero() {
		return errors.New("outbox acknowledgement time is required")
	}
	item.State = OutboxHistory
	item.UpdatedAt = now.UTC()
	item.LeaseUntil = time.Time{}
	item.NextAttemptAt = time.Time{}
	item.LastError = ""
	return outbox.transition(OutboxInflight, OutboxHistory, item)
}

func (outbox *Outbox) Nack(
	item *OutboxItem,
	cause error,
	now time.Time,
	backoff []time.Duration,
) (OutboxState, error) {
	if item == nil || item.State != OutboxInflight {
		return "", errors.New("only an inflight outbox item can be rejected")
	}
	if cause == nil {
		return "", errors.New("outbox rejection cause is required")
	}
	if now.IsZero() {
		return "", errors.New("outbox rejection time is required")
	}
	item.Attempts++
	item.UpdatedAt = now.UTC()
	item.LeaseUntil = time.Time{}
	item.LastError = truncatePersistedError(cause)
	if item.Attempts >= len(backoff) {
		item.State = OutboxDead
		item.NextAttemptAt = time.Time{}
		return OutboxDead, outbox.transition(OutboxInflight, OutboxDead, item)
	}
	item.State = OutboxPending
	item.NextAttemptAt = now.UTC().Add(backoff[item.Attempts-1])
	return OutboxPending, outbox.transition(OutboxInflight, OutboxPending, item)
}

func (outbox *Outbox) Recover(now time.Time) (int, error) {
	if now.IsZero() {
		return 0, errors.New("outbox recovery time is required")
	}
	items, err := outbox.list(OutboxInflight)
	if err != nil {
		return 0, err
	}
	now = now.UTC()
	recovered := 0
	for index := range items {
		item := &items[index]
		completed := false
		for _, state := range []OutboxState{OutboxPending, OutboxHistory, OutboxDead} {
			if _, err := os.Stat(outbox.path(state, item.ID)); err == nil {
				completed = true
				break
			}
		}
		if completed {
			if err := removePrivateFile(outbox.path(OutboxInflight, item.ID)); err != nil {
				return recovered, err
			}
			continue
		}
		if !item.LeaseUntil.IsZero() && item.LeaseUntil.After(now) {
			continue
		}
		item.State = OutboxPending
		item.UpdatedAt = now
		item.LeaseUntil = time.Time{}
		if err := outbox.transition(OutboxInflight, OutboxPending, item); err != nil {
			return recovered, err
		}
		recovered++
	}
	return recovered, nil
}

func (metadata SignatureMetadata) Validate(envelope Envelope, exactBody []byte) error {
	if err := validateIdentifier("signature keyId", metadata.KeyID); err != nil {
		return err
	}
	if !metadata.SentAt.Equal(envelope.SentAt) {
		return errors.New("signature sentAt does not match relay envelope")
	}
	if metadata.Nonce != envelope.Nonce {
		return errors.New("signature nonce does not match relay envelope")
	}
	if len(metadata.Signature) != 64 {
		return errors.New("signature metadata requires an Ed25519 signature")
	}
	if _, err := SigningMaterial(
		metadata.Method,
		metadata.Target,
		metadata.SentAt,
		metadata.Nonce,
		exactBody,
	); err != nil {
		return err
	}
	return nil
}

func (outbox *Outbox) transition(
	source OutboxState,
	destination OutboxState,
	item *OutboxItem,
) error {
	if err := outbox.writeNew(destination, item); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		existing, readErr := outbox.read(destination, item.ID)
		if readErr != nil || existing.BodyDigest != item.BodyDigest {
			return errors.New("relay outbox transition destination conflicts")
		}
	}
	if err := removePrivateFile(outbox.path(source, item.ID)); err != nil {
		return fmt.Errorf("remove relay outbox source %s: %w", source, err)
	}
	return nil
}

func (outbox *Outbox) writeNew(state OutboxState, item *OutboxItem) error {
	if item == nil || !validOutboxStates[state] || item.State != state {
		return errors.New("invalid relay outbox write")
	}
	return writeNewPrivateJSON(
		outbox.path(state, item.ID),
		filepath.Join(outbox.root, "tmp"),
		item,
	)
}

func (outbox *Outbox) read(state OutboxState, id string) (OutboxItem, error) {
	value, err := os.ReadFile(outbox.path(state, id))
	if err != nil {
		return OutboxItem{}, err
	}
	var item OutboxItem
	if err := strictJSON(value, &item); err != nil {
		return OutboxItem{}, err
	}
	if err := item.validatePersisted(state); err != nil {
		return OutboxItem{}, err
	}
	return item, nil
}

func (outbox *Outbox) list(state OutboxState) ([]OutboxItem, error) {
	if !validOutboxStates[state] {
		return nil, fmt.Errorf("unsupported outbox state %q", state)
	}
	entries, err := os.ReadDir(filepath.Join(outbox.root, string(state)))
	if err != nil {
		return nil, err
	}
	items := make([]OutboxItem, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		item, err := outbox.read(state, id)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	sort.Slice(items, func(left, right int) bool {
		return items[left].CreatedAt.Before(items[right].CreatedAt)
	})
	return items, nil
}

func (item OutboxItem) validatePersisted(expectedState OutboxState) error {
	if item.Version != outboxVersion ||
		item.ID == "" ||
		item.State != expectedState ||
		item.DeliveryKey == "" ||
		item.BodyDigest == "" ||
		len(item.ExactBody) == 0 ||
		len(item.ExactBody) > MaxBodyBytes ||
		item.CreatedAt.IsZero() ||
		item.UpdatedAt.IsZero() {
		return errors.New("invalid persisted relay outbox item")
	}
	envelope, err := Decode(item.ExactBody)
	if err != nil {
		return err
	}
	if envelope.Event.PrivacyLevel != event.PrivacyMetadataOnly ||
		envelope.Delivery.Key != item.DeliveryKey ||
		bodyDigest(item.ExactBody) != item.BodyDigest {
		return errors.New("persisted relay outbox item integrity check failed")
	}
	return item.Signature.Validate(envelope, item.ExactBody)
}

func (outbox *Outbox) path(state OutboxState, id string) string {
	return filepath.Join(outbox.root, string(state), id+".json")
}

func cloneSignature(value SignatureMetadata) SignatureMetadata {
	value.Signature = bytes.Clone(value.Signature)
	return value
}

func truncatePersistedError(err error) string {
	if err == nil {
		return ""
	}
	value := err.Error()
	if len(value) > maxPersistedErrorBytes {
		value = value[:maxPersistedErrorBytes]
	}
	return value
}

func validOutboxID(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') &&
			(character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
