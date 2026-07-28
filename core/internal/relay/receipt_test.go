package relay

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/liming0791/agentbell/core/internal/event"
)

func TestCommitIngressIsDurableAndIdempotent(t *testing.T) {
	store, err := OpenReceiptStore(filepath.Join(t.TempDir(), "receipts"))
	if err != nil {
		t.Fatal(err)
	}
	envelope := validEnvelope(t)
	body := encodedEnvelope(t, envelope)
	now := envelope.SentAt
	var enqueues atomic.Int32
	enqueue := func(received Envelope, exactBody []byte) (string, error) {
		enqueues.Add(1)
		if received.Delivery.Key != envelope.Delivery.Key ||
			string(exactBody) != string(body) {
			t.Fatal("enqueue did not receive exact ingress")
		}
		return "local-queue-item-1", nil
	}
	first, duplicate, err := store.CommitIngress(body, now, enqueue)
	if err != nil || duplicate || !first.ACKEligible() {
		t.Fatalf("first commit: receipt=%#v duplicate=%v err=%v", first, duplicate, err)
	}
	second, duplicate, err := store.CommitIngress(body, now.Add(time.Second), enqueue)
	if err != nil || !duplicate || !second.ACKEligible() {
		t.Fatalf("duplicate commit: receipt=%#v duplicate=%v err=%v", second, duplicate, err)
	}
	if first != second || enqueues.Load() != 1 {
		t.Fatalf("duplicate was not stable: first=%#v second=%#v calls=%d", first, second, enqueues.Load())
	}

	conflictingBody := append(append([]byte(nil), body...), '\n')
	if _, _, err := store.CommitIngress(
		conflictingBody,
		now.Add(2*time.Second),
		enqueue,
	); !errors.Is(err, ErrReceiptConflict) {
		t.Fatalf("expected receipt conflict, got %v", err)
	}
}

func TestCommitIngressConcurrentDuplicateEnqueuesOnce(t *testing.T) {
	root := filepath.Join(t.TempDir(), "receipts")
	envelope := validEnvelope(t)
	body := encodedEnvelope(t, envelope)
	var enqueues atomic.Int32
	enqueue := func(Envelope, []byte) (string, error) {
		enqueues.Add(1)
		time.Sleep(5 * time.Millisecond)
		return "local-queue-item-1", nil
	}
	const workers = 24
	var wait sync.WaitGroup
	var failures atomic.Int32
	wait.Add(workers)
	for range workers {
		go func() {
			defer wait.Done()
			store, openErr := OpenReceiptStore(root)
			if openErr != nil {
				failures.Add(1)
				return
			}
			receipt, _, commitErr := store.CommitIngress(
				body,
				envelope.SentAt,
				enqueue,
			)
			if commitErr != nil || !receipt.ACKEligible() {
				failures.Add(1)
			}
		}()
	}
	wait.Wait()
	if failures.Load() != 0 || enqueues.Load() != 1 {
		t.Fatalf("concurrent commit: failures=%d enqueues=%d", failures.Load(), enqueues.Load())
	}
}

func TestCommitIngressRetriesUncommittedReceipt(t *testing.T) {
	store, err := OpenReceiptStore(filepath.Join(t.TempDir(), "receipts"))
	if err != nil {
		t.Fatal(err)
	}
	envelope := validEnvelope(t)
	body := encodedEnvelope(t, envelope)
	if receipt, _, err := store.CommitIngress(
		body,
		envelope.SentAt,
		func(Envelope, []byte) (string, error) {
			return "", errors.New("local queue unavailable")
		},
	); err == nil || receipt.ACKEligible() {
		t.Fatalf("failed local enqueue became ACK eligible: %#v err=%v", receipt, err)
	}
	receipt, duplicate, err := store.CommitIngress(
		body,
		envelope.SentAt.Add(time.Second),
		func(Envelope, []byte) (string, error) {
			return "local-queue-item-2", nil
		},
	)
	if err != nil || !duplicate || !receipt.ACKEligible() {
		t.Fatalf("retry commit: receipt=%#v duplicate=%v err=%v", receipt, duplicate, err)
	}
}

func TestCommitIngressEnforcesMetadataOnlyAndBodyLimit(t *testing.T) {
	store, err := OpenReceiptStore(filepath.Join(t.TempDir(), "receipts"))
	if err != nil {
		t.Fatal(err)
	}
	envelope := validEnvelope(t)
	envelope.Event.PrivacyLevel = event.PrivacySummary
	envelope.Event.Summary = "must not cross relay"
	body := encodedEnvelope(t, envelope)
	if _, _, err := store.CommitIngress(
		body,
		envelope.SentAt,
		func(Envelope, []byte) (string, error) { return "unexpected", nil },
	); err == nil {
		t.Fatal("non-metadata event must fail")
	}
	if _, _, err := store.CommitIngress(
		make([]byte, MaxBodyBytes+1),
		time.Now(),
		func(Envelope, []byte) (string, error) { return "unexpected", nil },
	); err == nil {
		t.Fatal("oversized ingress must fail")
	}
}

func TestReceiptFilesArePrivate(t *testing.T) {
	root := filepath.Join(t.TempDir(), "receipts")
	store, err := OpenReceiptStore(root)
	if err != nil {
		t.Fatal(err)
	}
	envelope := validEnvelope(t)
	body := encodedEnvelope(t, envelope)
	receipt, _, err := store.CommitIngress(
		body,
		envelope.SentAt,
		func(Envelope, []byte) (string, error) { return "local-private", nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	value, err := os.ReadFile(store.committedPath(receipt.ID))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(value), "PRIVATE KEY") || !receipt.ACKEligible() {
		t.Fatal("receipt leaked private material or was not committed")
	}
}

func TestReceiptStoreListsCommittedMetadataWithoutEnvelope(t *testing.T) {
	store, err := OpenReceiptStore(filepath.Join(t.TempDir(), "receipts"))
	if err != nil {
		t.Fatal(err)
	}
	envelope := validEnvelope(t)
	body := encodedEnvelope(t, envelope)
	if _, _, err := store.CommitIngress(
		body,
		envelope.SentAt,
		func(Envelope, []byte) (string, error) {
			return "queue-list-one", nil
		},
	); err != nil {
		t.Fatal(err)
	}
	receipts, err := store.ListCommitted()
	if err != nil {
		t.Fatal(err)
	}
	if len(receipts) != 1 ||
		receipts[0].OriginID != envelope.Origin.ID ||
		receipts[0].LocalQueueID != "queue-list-one" {
		t.Fatalf("receipts = %#v", receipts)
	}
	encoded, err := json.Marshal(receipts)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(`"event"`)) ||
		bytes.Contains(encoded, []byte(`"exactBody"`)) {
		t.Fatalf("receipt list leaked relay body: %s", encoded)
	}
}
