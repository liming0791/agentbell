package queue

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func target(channelID, templateID string) DeliveryTarget {
	return DeliveryTarget{ChannelID: channelID, TemplateID: templateID}
}

func newClaimedItem(t *testing.T, queueValue *Queue, key string, now time.Time) *Item {
	t.Helper()
	if _, _, err := queueValue.Enqueue(notification(key), now); err != nil {
		t.Fatal(err)
	}
	item, err := queueValue.Claim(now, time.Minute)
	if err != nil || item == nil {
		t.Fatalf("claim: item=%#v err=%v", item, err)
	}
	return item
}

func TestLegacyEnvelopeWithoutLedgerRemainsReadable(t *testing.T) {
	queueValue, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	item := newClaimedItem(t, queueValue, "legacy-ledger", now)
	if item.QueueVersion != 1 || item.Ledger != nil || item.Disposition != "" {
		t.Fatalf("legacy item changed shape: %#v", item.Envelope)
	}
	if _, err := queueValue.Nack(
		item,
		errors.New("legacy retry"),
		now,
		[]time.Duration{time.Second, time.Second},
	); err != nil {
		t.Fatal(err)
	}
	item, err = queueValue.Claim(now.Add(2*time.Second), time.Minute)
	if err != nil || item == nil {
		t.Fatalf("claim legacy retry: item=%#v err=%v", item, err)
	}
	if err := queueValue.Ack(item, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	history, err := queueValue.List(StateSucceeded)
	if err != nil || len(history) != 1 || history[0].Ledger != nil {
		t.Fatalf("legacy history: %#v err=%v", history, err)
	}
}

func TestResolveTargetsSnapshotsOnce(t *testing.T) {
	queueValue, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	item := newClaimedItem(t, queueValue, "resolve-once", now)
	original := []DeliveryTarget{target("channel-a", "template-a"), target("channel-b", "template-b")}
	ready, err := queueValue.ResolveTargets(item, original, now)
	if err != nil || len(ready) != 2 {
		t.Fatalf("first resolve: ready=%#v err=%v", ready, err)
	}
	if item.Disposition != DispositionPending || len(item.Ledger) != 2 {
		t.Fatalf("ledger was not persisted: %#v", item.Envelope)
	}

	ready, err = queueValue.ResolveTargets(
		item,
		[]DeliveryTarget{target("new-channel", "new-template")},
		now.Add(time.Second),
	)
	if err != nil || len(ready) != 2 {
		t.Fatalf("second resolve: ready=%#v err=%v", ready, err)
	}
	if ready[0] != original[0] || ready[1] != original[1] {
		t.Fatalf("targets were re-resolved: %#v", ready)
	}

	inflight, err := queueValue.List(StateInflight)
	if err != nil || len(inflight) != 1 || len(inflight[0].Ledger) != 2 {
		t.Fatalf("persisted inflight ledger: %#v err=%v", inflight, err)
	}
}

func TestResolveTargetsRejectsDuplicateAndIsAtomic(t *testing.T) {
	root := t.TempDir()
	queueValue, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	item := newClaimedItem(t, queueValue, "resolve-atomic", now)
	duplicate := []DeliveryTarget{target("channel", "template"), target("channel", "template")}
	if _, err := queueValue.ResolveTargets(item, duplicate, now); err == nil {
		t.Fatal("expected duplicate target error")
	}
	if item.Ledger != nil {
		t.Fatalf("duplicate target mutated item: %#v", item.Ledger)
	}

	tmpDirectory := filepath.Join(root, "tmp")
	if err := os.Remove(tmpDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmpDirectory, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := queueValue.ResolveTargets(
		item,
		[]DeliveryTarget{target("channel", "template")},
		now,
	); err == nil {
		t.Fatal("expected atomic write failure")
	}
	if item.Ledger != nil || item.Disposition != "" {
		t.Fatalf("failed write mutated item: %#v", item.Envelope)
	}

	value, err := os.ReadFile(filepath.Join(root, "inflight", item.FileName))
	if err != nil {
		t.Fatal(err)
	}
	var persisted Envelope
	if err := json.Unmarshal(value, &persisted); err != nil {
		t.Fatal(err)
	}
	if persisted.Ledger != nil || persisted.Disposition != "" {
		t.Fatalf("failed write mutated disk: %#v", persisted)
	}
}

func TestPartialSuccessRetryNeverReturnsSucceededTarget(t *testing.T) {
	queueValue, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	item := newClaimedItem(t, queueValue, "partial-retry", now)
	first := target("channel-a", "template-a")
	second := target("channel-b", "template-b")
	if _, err := queueValue.ResolveTargets(item, []DeliveryTarget{first, second}, now); err != nil {
		t.Fatal(err)
	}
	if err := queueValue.AckTarget(item, first, "message-a", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	nextAttempt := now.Add(time.Hour)
	if err := queueValue.NackTarget(
		item,
		second,
		errors.New("temporary"),
		nextAttempt,
		now.Add(2*time.Second),
	); err != nil {
		t.Fatal(err)
	}
	if item.Disposition != DispositionPartial {
		t.Fatalf("unexpected partial disposition: %q", item.Disposition)
	}
	ready, err := queueValue.ResolveTargets(item, nil, now.Add(time.Minute))
	if err != nil || len(ready) != 0 {
		t.Fatalf("future target became ready: %#v err=%v", ready, err)
	}
	if err := queueValue.Defer(item, now.Add(3*time.Second), nextAttempt); err != nil {
		t.Fatal(err)
	}
	if item.Attempts != 0 || item.Ledger[0].Attempts != 1 || item.Ledger[1].Attempts != 1 {
		t.Fatalf("defer changed attempts: %#v", item.Envelope)
	}

	item, err = queueValue.Claim(nextAttempt, time.Minute)
	if err != nil || item == nil {
		t.Fatalf("claim deferred item: %#v err=%v", item, err)
	}
	ready, err = queueValue.ResolveTargets(
		item,
		[]DeliveryTarget{target("replacement", "replacement")},
		nextAttempt,
	)
	if err != nil || len(ready) != 1 || ready[0] != second {
		t.Fatalf("successful target would be resent: %#v err=%v", ready, err)
	}
	if err := queueValue.AckTarget(item, second, "message-b", nextAttempt); err != nil {
		t.Fatal(err)
	}
	if item.Disposition != DispositionSucceeded {
		t.Fatalf("unexpected completed disposition: %q", item.Disposition)
	}
	if err := queueValue.Ack(item, nextAttempt); err != nil {
		t.Fatal(err)
	}
	history, err := queueValue.List(StateSucceeded)
	if err != nil || len(history) != 1 {
		t.Fatalf("history: %#v err=%v", history, err)
	}
	if history[0].Ledger[0].Attempts != 1 || history[0].Ledger[0].MessageID != "message-a" {
		t.Fatalf("successful target was changed: %#v", history[0].Ledger)
	}
}

func TestEnvelopeAckRequiresAllTargetsTerminal(t *testing.T) {
	queueValue, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	item := newClaimedItem(t, queueValue, "terminal-ack", now)
	if _, err := queueValue.ResolveTargets(
		item,
		[]DeliveryTarget{target("channel", "template")},
		now,
	); err != nil {
		t.Fatal(err)
	}
	if err := queueValue.Ack(item, now); err == nil {
		t.Fatal("expected non-terminal ledger ack error")
	}
	stats, err := queueValue.Stats()
	if err != nil || stats.Inflight != 1 || stats.History != 0 {
		t.Fatalf("failed ack changed queue: %#v err=%v", stats, err)
	}
}

func TestDeadTargetCanFinishEnvelope(t *testing.T) {
	queueValue, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	item := newClaimedItem(t, queueValue, "target-dead", now)
	first := target("channel-a", "template")
	second := target("channel-b", "template")
	if _, err := queueValue.ResolveTargets(item, []DeliveryTarget{first, second}, now); err != nil {
		t.Fatal(err)
	}
	if err := queueValue.AckTarget(item, first, "message", now); err != nil {
		t.Fatal(err)
	}
	if err := queueValue.DeadTarget(item, second, errors.New("permanent"), now); err != nil {
		t.Fatal(err)
	}
	if item.Disposition != DispositionPartial || item.Ledger[1].State != DeliveryDead {
		t.Fatalf("unexpected terminal ledger: %#v", item.Envelope)
	}
	if err := queueValue.Ack(item, now); err != nil {
		t.Fatal(err)
	}
	history, err := queueValue.List(StateSucceeded)
	if err != nil || len(history) != 1 || history[0].Disposition != DispositionPartial {
		t.Fatalf("terminal history: %#v err=%v", history, err)
	}
}

func TestIllegalTargetTransitionsAreRejected(t *testing.T) {
	queueValue, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	item := newClaimedItem(t, queueValue, "illegal-target", now)
	first := target("channel-a", "template")
	second := target("channel-b", "template")
	if _, err := queueValue.ResolveTargets(item, []DeliveryTarget{first, second}, now); err != nil {
		t.Fatal(err)
	}
	if err := queueValue.AckTarget(item, first, "message", now); err != nil {
		t.Fatal(err)
	}
	if err := queueValue.NackTarget(item, first, errors.New("again"), now, now); err == nil {
		t.Fatal("expected nack of succeeded target to fail")
	}
	if err := queueValue.DeadTarget(item, first, errors.New("again"), now); err == nil {
		t.Fatal("expected dead of succeeded target to fail")
	}
	if err := queueValue.DeadTarget(item, second, errors.New("permanent"), now); err != nil {
		t.Fatal(err)
	}
	if err := queueValue.AckTarget(item, second, "late", now); err == nil {
		t.Fatal("expected ack of dead target to fail")
	}
	if _, err := queueValue.Nack(item, errors.New("envelope"), now, []time.Duration{time.Second}); err == nil {
		t.Fatal("expected envelope nack with ledger to fail")
	}
	if err := queueValue.Defer(item, now, now.Add(time.Hour)); err == nil {
		t.Fatal("expected terminal ledger defer to fail")
	}
}

func TestInvalidLedgerFilesAreQuarantined(t *testing.T) {
	tests := map[string][]DeliveryLedgerEntry{
		"duplicate": {
			{ChannelID: "channel", TemplateID: "template", State: DeliveryPending},
			{ChannelID: "channel", TemplateID: "template", State: DeliveryPending},
		},
		"invalid-state": {
			{ChannelID: "channel", TemplateID: "template", State: DeliveryState("unknown")},
		},
	}
	for name, ledger := range tests {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			queueValue, err := Open(root)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			envelope := Envelope{
				QueueVersion: QueueVersion,
				ID:           "invalid-" + name,
				State:        StatePending,
				Event:        notification("invalid-" + name),
				CreatedAt:    now,
				UpdatedAt:    now,
				Ledger:       ledger,
				Disposition:  DispositionPending,
			}
			value, err := json.Marshal(envelope)
			if err != nil {
				t.Fatal(err)
			}
			fileName := name + ".json"
			if err := os.WriteFile(filepath.Join(root, "pending", fileName), value, 0o600); err != nil {
				t.Fatal(err)
			}
			items, err := queueValue.List(StatePending)
			if err != nil || len(items) != 0 {
				t.Fatalf("invalid item returned: %#v err=%v", items, err)
			}
			if _, err := os.Stat(filepath.Join(root, "dead", fileName+".corrupt")); err != nil {
				t.Fatalf("invalid ledger was not quarantined: %v", err)
			}
		})
	}
}

func TestRollbackPreflightReportsPartialSuccess(t *testing.T) {
	queueValue, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	item := newClaimedItem(t, queueValue, "rollback-partial", now)
	first := target("channel-a", "template")
	second := target("channel-b", "template")
	if _, err := queueValue.ResolveTargets(item, []DeliveryTarget{first, second}, now); err != nil {
		t.Fatal(err)
	}
	if err := queueValue.AckTarget(item, first, "message", now); err != nil {
		t.Fatal(err)
	}
	if err := queueValue.NackTarget(
		item,
		second,
		errors.New("temporary"),
		now.Add(time.Hour),
		now,
	); err != nil {
		t.Fatal(err)
	}
	if err := queueValue.Defer(item, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	preflight, err := queueValue.PreflightRollback()
	if err != nil {
		t.Fatal(err)
	}
	if !preflight.HasPartialSuccess || len(preflight.Items) != 1 {
		t.Fatalf("partial success was not reported: %#v", preflight)
	}
	if preflight.Items[0].ID != item.ID ||
		preflight.Items[0].SucceededTargets != 1 ||
		preflight.Items[0].RemainingTargets != 1 {
		t.Fatalf("unexpected rollback detail: %#v", preflight.Items[0])
	}
}

func TestRecoverExpiredPreservesSuccessfulTargets(t *testing.T) {
	queueValue, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	item := newClaimedItem(t, queueValue, "recover-ledger", now)
	first := target("channel-a", "template")
	second := target("channel-b", "template")
	if _, err := queueValue.ResolveTargets(item, []DeliveryTarget{first, second}, now); err != nil {
		t.Fatal(err)
	}
	if err := queueValue.AckTarget(item, first, "message", now); err != nil {
		t.Fatal(err)
	}
	recovered, err := queueValue.RecoverExpired(now.Add(2 * time.Minute))
	if err != nil || recovered != 1 {
		t.Fatalf("recover: recovered=%d err=%v", recovered, err)
	}
	item, err = queueValue.Claim(now.Add(2*time.Minute), time.Minute)
	if err != nil || item == nil {
		t.Fatalf("claim recovered: item=%#v err=%v", item, err)
	}
	ready, err := queueValue.ResolveTargets(item, nil, now.Add(2*time.Minute))
	if err != nil || len(ready) != 1 || ready[0] != second {
		t.Fatalf("recovery would resend successful target: %#v err=%v", ready, err)
	}
}
