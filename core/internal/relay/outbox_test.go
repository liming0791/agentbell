package relay

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/liming0791/agentbell/core/internal/event"
)

func signedOutboxInput(t *testing.T) ([]byte, SignatureMetadata) {
	t.Helper()
	envelope := validEnvelope(t)
	body := encodedEnvelope(t, envelope)
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := Sign(
		privateKey,
		"POST",
		"/v1/relay/events",
		envelope.SentAt,
		envelope.Nonce,
		body,
	)
	if err != nil {
		t.Fatal(err)
	}
	return body, SignatureMetadata{
		KeyID:     "key-one",
		Method:    "POST",
		Target:    "/v1/relay/events",
		SentAt:    envelope.SentAt,
		Nonce:     envelope.Nonce,
		Signature: signature,
	}
}

func TestOutboxEnqueueClaimAndAckPreservesExactBody(t *testing.T) {
	root := filepath.Join(t.TempDir(), "outbox")
	outbox, err := OpenOutbox(root)
	if err != nil {
		t.Fatal(err)
	}
	body, signature := signedOutboxInput(t)
	now := time.Date(2026, 7, 27, 3, 0, 0, 0, time.UTC)
	id, duplicate, err := outbox.Enqueue(body, signature, now)
	if err != nil || duplicate || id == "" {
		t.Fatalf("enqueue: id=%q duplicate=%v err=%v", id, duplicate, err)
	}
	secondID, duplicate, err := outbox.Enqueue(body, signature, now.Add(time.Second))
	if err != nil || !duplicate || secondID != id {
		t.Fatalf("duplicate enqueue: id=%q duplicate=%v err=%v", secondID, duplicate, err)
	}
	item, err := outbox.Claim(now, time.Minute)
	if err != nil || item == nil {
		t.Fatalf("claim: item=%#v err=%v", item, err)
	}
	if !bytes.Equal(item.ExactBody, body) || !bytes.Equal(item.Signature.Signature, signature.Signature) {
		t.Fatal("outbox changed exact body or signature metadata")
	}
	if err := outbox.Ack(item, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "history", id+".json")); err != nil {
		t.Fatalf("history item missing: %v", err)
	}
}

func TestOutboxStatusAndStatsExposeOnlyLifecycleMetadata(t *testing.T) {
	outbox, err := OpenOutbox(filepath.Join(t.TempDir(), "outbox"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 27, 3, 0, 0, 0, time.UTC)
	body, signature := signedOutboxInput(t)
	id, _, err := outbox.Enqueue(body, signature, now)
	if err != nil {
		t.Fatal(err)
	}
	state, err := outbox.Status(id)
	if err != nil || state != OutboxPending {
		t.Fatalf("pending state=%q err=%v", state, err)
	}
	stats, err := outbox.Stats()
	if err != nil || stats.Pending != 1 || stats.Total != 1 {
		t.Fatalf("pending stats=%#v err=%v", stats, err)
	}
	item, err := outbox.Claim(now.Add(time.Second), time.Minute)
	if err != nil || item == nil {
		t.Fatalf("claim item=%#v err=%v", item, err)
	}
	if err := outbox.Ack(item, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	state, err = outbox.Status(id)
	if err != nil || state != OutboxHistory {
		t.Fatalf("history state=%q err=%v", state, err)
	}
	stats, err = outbox.Stats()
	if err != nil || stats.History != 1 || stats.Retrying != 0 ||
		stats.Total != 1 {
		t.Fatalf("history stats=%#v err=%v", stats, err)
	}
	for _, unsafe := range []string{"", "../private", id + ".json", "not-hex"} {
		if _, err := outbox.Status(unsafe); err == nil {
			t.Fatalf("unsafe id %q was accepted", unsafe)
		}
	}
}

func TestOutboxLookupDeliveryReusesDurableProducerIdentity(t *testing.T) {
	outbox, err := OpenOutbox(filepath.Join(t.TempDir(), "outbox"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 27, 3, 0, 0, 0, time.UTC)
	body, signature := signedOutboxInput(t)
	envelope, err := Decode(body)
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := outbox.Enqueue(body, signature, now)
	if err != nil {
		t.Fatal(err)
	}
	foundID, state, found, err := outbox.LookupDelivery(
		envelope.TeamID,
		envelope.Origin.ID,
		envelope.Delivery.Key,
	)
	if err != nil || !found || foundID != id || state != OutboxPending {
		t.Fatalf(
			"pending lookup id=%q state=%q found=%v err=%v",
			foundID,
			state,
			found,
			err,
		)
	}
	item, err := outbox.Claim(now, time.Minute)
	if err != nil || item == nil {
		t.Fatalf("claim item=%#v err=%v", item, err)
	}
	if err := outbox.Ack(item, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	foundID, state, found, err = outbox.LookupDelivery(
		envelope.TeamID,
		envelope.Origin.ID,
		envelope.Delivery.Key,
	)
	if err != nil || !found || foundID != id || state != OutboxHistory {
		t.Fatalf(
			"history lookup id=%q state=%q found=%v err=%v",
			foundID,
			state,
			found,
			err,
		)
	}
	missingKey, err := DeriveDeliveryKey(
		envelope.TeamID,
		envelope.Origin.ID,
		"producer-missing",
	)
	if err != nil {
		t.Fatal(err)
	}
	_, state, found, err = outbox.LookupDelivery(
		envelope.TeamID,
		envelope.Origin.ID,
		missingKey,
	)
	if err != nil || found || state != "" {
		t.Fatalf("missing lookup state=%q found=%v err=%v", state, found, err)
	}
	for _, test := range []struct {
		team     string
		origin   string
		delivery string
	}{
		{"", envelope.Origin.ID, envelope.Delivery.Key},
		{envelope.TeamID, "", envelope.Delivery.Key},
		{envelope.TeamID, envelope.Origin.ID, "not-a-delivery-key"},
	} {
		if _, _, _, err := outbox.LookupDelivery(
			test.team,
			test.origin,
			test.delivery,
		); err == nil {
			t.Fatalf("unsafe lookup was accepted: %#v", test)
		}
	}
	if _, _, _, err := (*Outbox)(nil).LookupDelivery(
		envelope.TeamID,
		envelope.Origin.ID,
		envelope.Delivery.Key,
	); err == nil {
		t.Fatal("nil outbox lookup was accepted")
	}
}

func TestOutboxNackRetriesThenDeadLetters(t *testing.T) {
	outbox, err := OpenOutbox(filepath.Join(t.TempDir(), "outbox"))
	if err != nil {
		t.Fatal(err)
	}
	body, signature := signedOutboxInput(t)
	now := time.Date(2026, 7, 27, 3, 0, 0, 0, time.UTC)
	if _, _, err := outbox.Enqueue(body, signature, now); err != nil {
		t.Fatal(err)
	}
	item, err := outbox.Claim(now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	state, err := outbox.Nack(item, errors.New("offline"), now, []time.Duration{time.Second, time.Second})
	if err != nil || state != OutboxPending {
		t.Fatalf("first nack: state=%s err=%v", state, err)
	}
	if item, err = outbox.Claim(now, time.Minute); err != nil || item != nil {
		t.Fatalf("backoff was ignored: item=%#v err=%v", item, err)
	}
	item, err = outbox.Claim(now.Add(time.Second), time.Minute)
	if err != nil || item == nil {
		t.Fatalf("retry claim: item=%#v err=%v", item, err)
	}
	state, err = outbox.Nack(
		item,
		errors.New(strings.Repeat("x", 5000)),
		now.Add(time.Second),
		[]time.Duration{time.Second, time.Second},
	)
	if err != nil || state != OutboxDead {
		t.Fatalf("dead nack: state=%s err=%v", state, err)
	}
	if len(item.LastError) > maxPersistedErrorBytes {
		t.Fatalf("persisted error was not truncated: %d", len(item.LastError))
	}
}

func TestOutboxRecoversExpiredInflightAndCrashDuplicate(t *testing.T) {
	root := filepath.Join(t.TempDir(), "outbox")
	outbox, err := OpenOutbox(root)
	if err != nil {
		t.Fatal(err)
	}
	body, signature := signedOutboxInput(t)
	now := time.Date(2026, 7, 27, 3, 0, 0, 0, time.UTC)
	id, _, err := outbox.Enqueue(body, signature, now)
	if err != nil {
		t.Fatal(err)
	}
	item, err := outbox.Claim(now, -time.Second)
	if err != nil || item == nil {
		t.Fatalf("claim: item=%#v err=%v", item, err)
	}
	recovered, err := outbox.Recover(now)
	if err != nil || recovered != 1 {
		t.Fatalf("recover: recovered=%d err=%v", recovered, err)
	}

	// Simulate a crash after the target pending file was committed but before
	// the old inflight copy was removed.
	reclaimed, err := outbox.Claim(now, -time.Second)
	if err != nil || reclaimed == nil {
		t.Fatalf("reclaim: item=%#v err=%v", reclaimed, err)
	}
	pendingCopy := *reclaimed
	pendingCopy.State = OutboxPending
	pendingCopy.LeaseUntil = time.Time{}
	if err := outbox.writeNew(OutboxPending, &pendingCopy); err != nil {
		t.Fatal(err)
	}
	recovered, err = outbox.Recover(now)
	if err != nil || recovered != 0 {
		t.Fatalf("crash duplicate recovery: recovered=%d err=%v", recovered, err)
	}
	if _, err := os.Stat(filepath.Join(root, "inflight", id+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale inflight copy survived: %v", err)
	}
}

func TestOutboxEnforcesPrivacySizeAndSignatureMetadata(t *testing.T) {
	outbox, err := OpenOutbox(filepath.Join(t.TempDir(), "outbox"))
	if err != nil {
		t.Fatal(err)
	}
	body, signature := signedOutboxInput(t)
	badSignature := signature
	badSignature.Signature = nil
	if _, _, err := outbox.Enqueue(body, badSignature, time.Now()); err == nil {
		t.Fatal("invalid signature metadata must fail")
	}
	envelope := validEnvelope(t)
	envelope.Event.PrivacyLevel = event.PrivacyFull
	envelope.Event.Summary = "secret"
	if _, _, err := outbox.Enqueue(
		encodedEnvelope(t, envelope),
		signature,
		time.Now(),
	); err == nil {
		t.Fatal("non-metadata event must fail")
	}
	if _, _, err := outbox.Enqueue(
		make([]byte, MaxBodyBytes+1),
		signature,
		time.Now(),
	); err == nil {
		t.Fatal("oversized body must fail")
	}
}

func TestOutboxUsesPrivatePermissionsAndStoresNoPrivateKey(t *testing.T) {
	root := filepath.Join(t.TempDir(), "outbox")
	outbox, err := OpenOutbox(root)
	if err != nil {
		t.Fatal(err)
	}
	body, signature := signedOutboxInput(t)
	id, _, err := outbox.Enqueue(body, signature, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	value, err := os.ReadFile(filepath.Join(root, "pending", id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(value), "private") {
		t.Fatal("outbox persisted private key material")
	}
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(filepath.Join(root, "pending", id+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("outbox item mode: %o", info.Mode().Perm())
	}
	for _, directory := range outboxDirectories {
		info, err := os.Stat(filepath.Join(root, directory))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("%s mode: %o", directory, info.Mode().Perm())
		}
	}
}

func TestOutboxBoundedEnqueueRejectsCapacityButAllowsDuplicate(t *testing.T) {
	body, signature := signedOutboxInput(t)
	root := t.TempDir()
	outbox, err := OpenOutbox(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, _, err := outbox.EnqueueBounded(
		body,
		signature,
		now,
		1,
	); !errors.Is(err, ErrOutboxCapacity) {
		t.Fatalf("capacity error = %v", err)
	}
	files, err := os.ReadDir(filepath.Join(root, "pending"))
	if err != nil || len(files) != 0 {
		t.Fatalf("rejected enqueue persisted files=%d err=%v", len(files), err)
	}
	id, duplicate, err := outbox.EnqueueBounded(
		body,
		signature,
		now,
		1<<20,
	)
	if err != nil || duplicate || id == "" {
		t.Fatalf("initial enqueue id=%q duplicate=%v err=%v", id, duplicate, err)
	}
	duplicateID, duplicate, err := outbox.EnqueueBounded(
		body,
		signature,
		now.Add(time.Second),
		1,
	)
	if err != nil || !duplicate || duplicateID != id {
		t.Fatalf(
			"duplicate enqueue id=%q duplicate=%v err=%v",
			duplicateID,
			duplicate,
			err,
		)
	}
}

func TestOutboxRetryUsageCountsOnlyRetryJSONAndValidatesBounds(t *testing.T) {
	root := t.TempDir()
	outbox, err := OpenOutbox(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := (*Outbox)(nil).EnqueueBounded(
		nil,
		SignatureMetadata{},
		time.Now(),
		1,
	); !errors.Is(err, ErrOutboxCapacity) {
		t.Fatalf("nil outbox error = %v", err)
	}
	if _, _, err := outbox.EnqueueBounded(
		nil,
		SignatureMetadata{},
		time.Now(),
		1<<20,
	); err == nil {
		t.Fatal("empty body was accepted")
	}
	pending := filepath.Join(root, string(OutboxPending))
	if err := os.WriteFile(
		filepath.Join(pending, "ignored.txt"),
		[]byte("ignored"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(pending, "ignored.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(pending, "counted.json"),
		[]byte("12345"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	usage, err := outbox.retryUsage()
	if err != nil || usage != 5 {
		t.Fatalf("usage=%d err=%v", usage, err)
	}
	broken := &Outbox{root: filepath.Join(root, "missing")}
	if _, err := broken.retryUsage(); err == nil {
		t.Fatal("missing outbox directories were accepted")
	}
}
