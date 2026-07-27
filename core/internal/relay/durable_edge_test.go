package relay

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDurableStoreConstructorsRejectEmptyRoots(t *testing.T) {
	if _, err := OpenNonceStore("", MinimumNonceRetention); err == nil {
		t.Fatal("empty nonce root must fail")
	}
	if _, err := OpenReceiptStore(""); err == nil {
		t.Fatal("empty receipt root must fail")
	}
	if _, err := OpenOutbox(""); err == nil {
		t.Fatal("empty outbox root must fail")
	}
}

func TestNonceCorruptReservationRemainsReplaySafeUntilRetention(t *testing.T) {
	root := filepath.Join(t.TempDir(), "nonces")
	store, err := OpenNonceStore(root, MinimumNonceRetention)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	nonce := "0123456789abcdef0123456789abcdef"
	path := store.path("peer-one", nonce)
	if err := os.WriteFile(path, []byte(`{"partial":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if accepted, err := store.Accept("peer-one", nonce, now); err != nil || accepted {
		t.Fatalf("partial reservation was not replay safe: accepted=%v err=%v", accepted, err)
	}
	if removed, err := store.Cleanup(now); err != nil || removed != 0 {
		t.Fatalf("fresh corrupt cleanup: removed=%d err=%v", removed, err)
	}
	old := now.Add(-MinimumNonceRetention - time.Second)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if removed, err := store.Cleanup(now); err != nil || removed != 1 {
		t.Fatalf("expired corrupt cleanup: removed=%d err=%v", removed, err)
	}
	if _, err := store.Cleanup(time.Time{}); err == nil {
		t.Fatal("zero cleanup time must fail")
	}
}

func TestCommitIngressRejectsIncompleteDurabilityInputs(t *testing.T) {
	store, err := OpenReceiptStore(filepath.Join(t.TempDir(), "receipts"))
	if err != nil {
		t.Fatal(err)
	}
	envelope := validEnvelope(t)
	body := encodedEnvelope(t, envelope)
	if _, _, err := store.CommitIngress(
		body,
		time.Time{},
		func(Envelope, []byte) (string, error) { return "id", nil },
	); err == nil {
		t.Fatal("zero commit time must fail")
	}
	if _, _, err := store.CommitIngress(body, envelope.SentAt, nil); err == nil {
		t.Fatal("nil durable enqueue must fail")
	}
	if receipt, _, err := store.CommitIngress(
		body,
		envelope.SentAt,
		func(Envelope, []byte) (string, error) { return "", nil },
	); err == nil || receipt.ACKEligible() {
		t.Fatalf("empty local queue id became eligible: %#v err=%v", receipt, err)
	}
	forged := Receipt{
		Version:      receiptVersion,
		State:        receiptStateCommitted,
		LocalQueueID: "not-durable",
		CommittedAt:  envelope.SentAt,
	}
	if forged.ACKEligible() {
		t.Fatal("an in-memory forged receipt became ACK eligible")
	}
}

func TestOutboxOperationValidationAndActiveLease(t *testing.T) {
	outbox, err := OpenOutbox(filepath.Join(t.TempDir(), "outbox"))
	if err != nil {
		t.Fatal(err)
	}
	body, signature := signedOutboxInput(t)
	now := time.Now().UTC()
	if _, _, err := outbox.Enqueue(body, signature, time.Time{}); err == nil {
		t.Fatal("zero enqueue time must fail")
	}
	if _, err := outbox.Claim(time.Time{}, time.Minute); err == nil {
		t.Fatal("zero claim time must fail")
	}
	if err := outbox.Ack(nil, now); err == nil {
		t.Fatal("nil ack item must fail")
	}
	if _, err := outbox.Nack(nil, errors.New("failure"), now, nil); err == nil {
		t.Fatal("nil nack item must fail")
	}
	if _, err := outbox.Recover(time.Time{}); err == nil {
		t.Fatal("zero recovery time must fail")
	}
	if err := outbox.writeNew(OutboxPending, nil); err == nil {
		t.Fatal("nil persisted item must fail")
	}
	if _, err := outbox.list(OutboxState("unknown")); err == nil {
		t.Fatal("unknown list state must fail")
	}

	if _, _, err := outbox.Enqueue(body, signature, now); err != nil {
		t.Fatal(err)
	}
	item, err := outbox.Claim(now, time.Hour)
	if err != nil || item == nil {
		t.Fatalf("claim: item=%#v err=%v", item, err)
	}
	if recovered, err := outbox.Recover(now.Add(time.Minute)); err != nil || recovered != 0 {
		t.Fatalf("active lease recovered: recovered=%d err=%v", recovered, err)
	}
	if _, err := outbox.Nack(item, nil, now, nil); err == nil {
		t.Fatal("nil nack cause must fail")
	}
	if _, err := outbox.Nack(item, errors.New("failure"), time.Time{}, nil); err == nil {
		t.Fatal("zero nack time must fail")
	}
	if err := outbox.Ack(item, time.Time{}); err == nil {
		t.Fatal("zero ack time must fail")
	}
}

func TestSignatureMetadataMustMatchEnvelope(t *testing.T) {
	body, valid := signedOutboxInput(t)
	envelope, err := Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*SignatureMetadata)
	}{
		{"key", func(value *SignatureMetadata) { value.KeyID = "" }},
		{"sentAt", func(value *SignatureMetadata) { value.SentAt = value.SentAt.Add(time.Second) }},
		{"nonce", func(value *SignatureMetadata) { value.Nonce = "abcdefabcdefabcdefabcdefabcdefab" }},
		{"method", func(value *SignatureMetadata) { value.Method = "\n" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			if err := candidate.Validate(envelope, body); err == nil {
				t.Fatal("expected signature metadata error")
			}
		})
	}
}

func TestOutboxTransitionRepairsCommittedTarget(t *testing.T) {
	root := filepath.Join(t.TempDir(), "outbox")
	outbox, err := OpenOutbox(root)
	if err != nil {
		t.Fatal(err)
	}
	body, signature := signedOutboxInput(t)
	now := time.Now().UTC()
	if _, _, err := outbox.Enqueue(body, signature, now); err != nil {
		t.Fatal(err)
	}
	item, err := outbox.Claim(now, time.Minute)
	if err != nil || item == nil {
		t.Fatalf("claim: item=%#v err=%v", item, err)
	}
	history := *item
	history.State = OutboxHistory
	history.UpdatedAt = now.Add(time.Second)
	history.LeaseUntil = time.Time{}
	if err := outbox.writeNew(OutboxHistory, &history); err != nil {
		t.Fatal(err)
	}
	if err := outbox.Ack(item, now.Add(time.Second)); err != nil {
		t.Fatalf("repair ack: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "inflight", item.ID+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inflight source survived repaired transition: %v", err)
	}
	if truncatePersistedError(nil) != "" {
		t.Fatal("nil error truncation must be empty")
	}
}

func TestStrictPersistedJSONRejectsTrailingData(t *testing.T) {
	var destination map[string]any
	if err := strictJSON([]byte(`{"ok":true} {}`), &destination); err == nil {
		t.Fatal("persisted trailing JSON must fail")
	}
	if err := strictJSON([]byte(`{"ok":`), &destination); err == nil {
		t.Fatal("malformed persisted JSON must fail")
	}
}

func TestReceiptReadRejectsTrailingData(t *testing.T) {
	store, err := OpenReceiptStore(filepath.Join(t.TempDir(), "receipts"))
	if err != nil {
		t.Fatal(err)
	}
	envelope := validEnvelope(t)
	body := encodedEnvelope(t, envelope)
	receipt, _, err := store.CommitIngress(
		body,
		envelope.SentAt,
		func(Envelope, []byte) (string, error) { return "local-one", nil },
	)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(store.committedPath(receipt.ID), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{}`); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.readCommitted(receipt.ID); err == nil {
		t.Fatal("receipt with trailing JSON must fail")
	}
}
