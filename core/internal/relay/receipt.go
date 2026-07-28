package relay

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/liming0791/agentbell/core/internal/event"
)

const (
	receiptVersion        = 1
	receiptStatePending   = "pending"
	receiptStateCommitted = "committed"
)

var ErrReceiptConflict = errors.New("relay receipt body digest conflict")

type Receipt struct {
	Version      int       `json:"version"`
	ID           string    `json:"id"`
	TeamID       string    `json:"teamId"`
	OriginID     string    `json:"originId"`
	DeliveryKey  string    `json:"deliveryKey"`
	BodyDigest   string    `json:"bodyDigest"`
	State        string    `json:"state"`
	LocalQueueID string    `json:"localQueueId,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
	CommittedAt  time.Time `json:"committedAt,omitempty"`

	durablyCommitted bool
}

// DurableEnqueue must return only after the local queue item is durably
// committed. It must be idempotent by Envelope.Delivery.Key because a crash
// after its return but before receipt commit causes CommitIngress to retry it.
type DurableEnqueue func(envelope Envelope, exactBody []byte) (string, error)

type ReceiptStore struct {
	root      string
	pending   string
	committed string
	temporary string
	locks     string
}

// LookupCommitted returns an existing durable receipt only when exactBody is
// byte-for-byte the body that originally committed it. It lets ingress return
// a stable ACK after an ACK-lost retry without re-enqueueing or weakening
// nonce replay protection for a different request.
func (store *ReceiptStore) LookupCommitted(
	exactBody []byte,
) (Receipt, bool, error) {
	envelope, err := Decode(exactBody)
	if err != nil {
		return Receipt{}, false, err
	}
	id := receiptID(envelope.TeamID, envelope.Origin.ID, envelope.Delivery.Key)
	receipt, err := store.readCommitted(id)
	if errors.Is(err, os.ErrNotExist) {
		return Receipt{}, false, nil
	}
	if err != nil {
		return Receipt{}, false, err
	}
	if receipt.BodyDigest != bodyDigest(exactBody) {
		return Receipt{}, true, ErrReceiptConflict
	}
	return receipt, true, nil
}

func (store *ReceiptStore) ListCommitted() ([]Receipt, error) {
	if store == nil || store.committed == "" {
		return nil, errors.New("relay receipt store is not initialized")
	}
	entries, err := os.ReadDir(store.committed)
	if err != nil {
		return nil, err
	}
	receipts := make([]Receipt, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := entry.Name()[:len(entry.Name())-len(".json")]
		receipt, err := store.readCommitted(id)
		if err != nil {
			return nil, fmt.Errorf(
				"read committed relay receipt metadata: %w",
				err,
			)
		}
		receipts = append(receipts, receipt)
	}
	sort.Slice(receipts, func(left, right int) bool {
		if receipts[left].CommittedAt.Equal(receipts[right].CommittedAt) {
			return receipts[left].ID < receipts[right].ID
		}
		return receipts[left].CommittedAt.Before(receipts[right].CommittedAt)
	})
	return receipts, nil
}

func OpenReceiptStore(root string) (*ReceiptStore, error) {
	if root == "" {
		return nil, errors.New("receipt store root is required")
	}
	store := &ReceiptStore{
		root:      root,
		pending:   filepath.Join(root, receiptStatePending),
		committed: filepath.Join(root, receiptStateCommitted),
		temporary: filepath.Join(root, "tmp"),
		locks:     filepath.Join(root, "locks"),
	}
	for _, directory := range []string{
		store.root,
		store.pending,
		store.committed,
		store.temporary,
		store.locks,
	} {
		if err := ensurePrivateDirectory(directory); err != nil {
			return nil, fmt.Errorf("create receipt store: %w", err)
		}
	}
	return store, nil
}

func (receipt Receipt) ACKEligible() bool {
	return receipt.Version == receiptVersion &&
		receipt.State == receiptStateCommitted &&
		receipt.LocalQueueID != "" &&
		!receipt.CommittedAt.IsZero() &&
		receipt.durablyCommitted
}

func (store *ReceiptStore) CommitIngress(
	exactBody []byte,
	now time.Time,
	enqueue DurableEnqueue,
) (Receipt, bool, error) {
	envelope, err := Decode(exactBody)
	if err != nil {
		return Receipt{}, false, err
	}
	if envelope.Event.PrivacyLevel != event.PrivacyMetadataOnly {
		return Receipt{}, false, errors.New("relay ingress only accepts metadata-only events")
	}
	if now.IsZero() {
		return Receipt{}, false, errors.New("receipt commit time is required")
	}
	if enqueue == nil {
		return Receipt{}, false, errors.New("durable local enqueue is required")
	}
	now = now.UTC()
	id := receiptID(envelope.TeamID, envelope.Origin.ID, envelope.Delivery.Key)
	digest := bodyDigest(exactBody)
	release, err := acquireStorageLock(filepath.Join(store.locks, id+".lock"))
	if err != nil {
		return Receipt{}, false, err
	}
	defer release()

	if existing, readErr := store.readCommitted(id); readErr == nil {
		if existing.BodyDigest != digest {
			return Receipt{}, true, ErrReceiptConflict
		}
		return existing, true, nil
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return Receipt{}, false, readErr
	}

	duplicate := false
	pending, readErr := store.readPending(id)
	switch {
	case readErr == nil:
		duplicate = true
		if pending.BodyDigest != digest {
			return Receipt{}, true, ErrReceiptConflict
		}
	case errors.Is(readErr, os.ErrNotExist):
		pending = Receipt{
			Version:     receiptVersion,
			ID:          id,
			TeamID:      envelope.TeamID,
			OriginID:    envelope.Origin.ID,
			DeliveryKey: envelope.Delivery.Key,
			BodyDigest:  digest,
			State:       receiptStatePending,
			CreatedAt:   now,
		}
		if err := writeNewPrivateJSON(
			store.pendingPath(id),
			store.temporary,
			pending,
		); err != nil {
			return Receipt{}, false, fmt.Errorf("persist pending relay receipt: %w", err)
		}
	default:
		return Receipt{}, false, readErr
	}

	localQueueID, err := enqueue(envelope, bytes.Clone(exactBody))
	if err != nil {
		return pending, duplicate, fmt.Errorf("durable local enqueue: %w", err)
	}
	if localQueueID == "" {
		return pending, duplicate, errors.New("durable local enqueue returned an empty id")
	}
	committed := pending
	committed.State = receiptStateCommitted
	committed.LocalQueueID = localQueueID
	committed.CommittedAt = now
	if err := writeNewPrivateJSON(
		store.committedPath(id),
		store.temporary,
		committed,
	); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return pending, duplicate, fmt.Errorf("commit relay receipt: %w", err)
		}
		existing, readErr := store.readCommitted(id)
		if readErr != nil {
			return pending, duplicate, readErr
		}
		if existing.BodyDigest != digest {
			return Receipt{}, true, ErrReceiptConflict
		}
		committed = existing
	} else {
		committed.durablyCommitted = true
	}
	if err := removePrivateFile(store.pendingPath(id)); err != nil {
		return Receipt{}, duplicate, fmt.Errorf("remove pending relay receipt: %w", err)
	}
	return committed, duplicate, nil
}

func (store *ReceiptStore) pendingPath(id string) string {
	return filepath.Join(store.pending, id+".json")
}

func (store *ReceiptStore) committedPath(id string) string {
	return filepath.Join(store.committed, id+".json")
}

func (store *ReceiptStore) readPending(id string) (Receipt, error) {
	return readReceipt(store.pendingPath(id), receiptStatePending)
}

func (store *ReceiptStore) readCommitted(id string) (Receipt, error) {
	return readReceipt(store.committedPath(id), receiptStateCommitted)
}

func readReceipt(path, expectedState string) (Receipt, error) {
	value, err := os.ReadFile(path)
	if err != nil {
		return Receipt{}, err
	}
	var receipt Receipt
	if err := strictJSON(value, &receipt); err != nil {
		return Receipt{}, fmt.Errorf("decode relay receipt: %w", err)
	}
	receipt.durablyCommitted = expectedState == receiptStateCommitted
	if receipt.Version != receiptVersion ||
		receipt.ID == "" ||
		receipt.State != expectedState ||
		receipt.TeamID == "" ||
		receipt.OriginID == "" ||
		receipt.DeliveryKey == "" ||
		receipt.BodyDigest == "" ||
		receipt.CreatedAt.IsZero() ||
		(expectedState == receiptStateCommitted && !receipt.ACKEligible()) {
		return Receipt{}, errors.New("invalid persisted relay receipt")
	}
	return receipt, nil
}

func receiptID(teamID, originID, deliveryKey string) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte("agentbell-relay-receipt-v1"))
	writeHashField(hash, teamID)
	writeHashField(hash, originID)
	writeHashField(hash, deliveryKey)
	return hex.EncodeToString(hash.Sum(nil))
}

func bodyDigest(exactBody []byte) string {
	sum := sha256.Sum256(exactBody)
	return "sha256:" + hex.EncodeToString(sum[:])
}
