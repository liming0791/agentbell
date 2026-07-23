package queue

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liming0791/agentbell/core/internal/event"
)

func notification(key string) event.Notification {
	return event.Notification{
		Version:        event.Version,
		Source:         "codex",
		Surface:        "cli",
		Runtime:        "host",
		Event:          event.EventTaskCompleted,
		Status:         event.StatusCompleted,
		OccurredAt:     time.Now().UTC(),
		IdempotencyKey: key,
		Priority:       event.PriorityNormal,
		PrivacyLevel:   event.PrivacyMetadataOnly,
		Project:        "agentbell",
	}
}

func TestQueueLifecycle(t *testing.T) {
	queue, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	id, duplicate, err := queue.Enqueue(notification("key-1"), now)
	if err != nil || duplicate || id == "" {
		t.Fatalf("enqueue failed: id=%q duplicate=%v err=%v", id, duplicate, err)
	}

	item, err := queue.Claim(now, time.Minute)
	if err != nil || item == nil || item.ID != id {
		t.Fatalf("claim failed: %#v %v", item, err)
	}
	if err := queue.Ack(item, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	stats, err := queue.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.History != 1 || stats.Pending != 0 || stats.Inflight != 0 {
		t.Fatalf("unexpected stats: %#v", stats)
	}
}

func TestQueueDeduplicatesConcurrentEnqueue(t *testing.T) {
	queue, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const workers = 16
	var wait sync.WaitGroup
	var mu sync.Mutex
	successes := 0
	duplicates := 0
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, duplicate, err := queue.Enqueue(notification("same-key"), time.Now())
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if duplicate {
				duplicates++
			} else {
				successes++
			}
		}()
	}
	wait.Wait()
	if successes != 1 || duplicates != workers-1 {
		t.Fatalf("successes=%d duplicates=%d", successes, duplicates)
	}
}

func TestQueueDeduplicatesAcrossIndependentInstances(t *testing.T) {
	root := t.TempDir()
	const workers = 16
	queues := make([]*Queue, 0, workers)
	for index := 0; index < workers; index++ {
		queueValue, err := Open(root)
		if err != nil {
			t.Fatal(err)
		}
		queues = append(queues, queueValue)
	}

	var wait sync.WaitGroup
	var mu sync.Mutex
	ids := make([]string, 0, workers)
	successes := 0
	duplicates := 0
	for index := range queues {
		wait.Add(1)
		go func(queueValue *Queue) {
			defer wait.Done()
			id, duplicate, err := queueValue.Enqueue(
				notification("cross-process-key"),
				time.Now(),
			)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			ids = append(ids, id)
			if duplicate {
				duplicates++
			} else {
				successes++
			}
		}(queues[index])
	}
	wait.Wait()
	if successes != 1 || duplicates != workers-1 || len(ids) != workers {
		t.Fatalf("successes=%d duplicates=%d ids=%#v", successes, duplicates, ids)
	}
	for _, id := range ids {
		if id == "" || id != ids[0] {
			t.Fatalf("reservation returned inconsistent ids: %#v", ids)
		}
	}
	pending, err := queues[0].List(StatePending)
	if err != nil || len(pending) != 1 {
		t.Fatalf("cross-instance pending: %#v err=%v", pending, err)
	}
}

func TestQueueDuplicateReturnsOriginalID(t *testing.T) {
	queueValue, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	firstID, duplicate, err := queueValue.Enqueue(notification("stable-key"), now)
	if err != nil || duplicate {
		t.Fatalf("first enqueue: id=%q duplicate=%v err=%v", firstID, duplicate, err)
	}
	secondID, duplicate, err := queueValue.Enqueue(
		notification("stable-key"),
		now.Add(time.Second),
	)
	if err != nil || !duplicate || secondID != firstID {
		t.Fatalf(
			"duplicate enqueue: first=%q second=%q duplicate=%v err=%v",
			firstID,
			secondID,
			duplicate,
			err,
		)
	}
}

func TestQueueRetryAndDeadLetter(t *testing.T) {
	queue, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	id, _, err := queue.Enqueue(notification("retry-key"), now)
	if err != nil {
		t.Fatal(err)
	}
	backoff := []time.Duration{time.Millisecond, time.Millisecond}

	item, _ := queue.Claim(now, time.Minute)
	state, err := queue.Nack(item, errors.New("temporary"), now, backoff)
	if err != nil || state != StatePending {
		t.Fatalf("first nack: %s %v", state, err)
	}
	item, _ = queue.Claim(now.Add(time.Second), time.Minute)
	state, err = queue.Nack(item, errors.New("permanent"), now.Add(time.Second), backoff)
	if err != nil || state != StateDead {
		t.Fatalf("second nack: %s %v", state, err)
	}

	if err := queue.Retry(id, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	stats, _ := queue.Stats()
	if stats.Pending != 1 || stats.Dead != 0 {
		t.Fatalf("unexpected stats after retry: %#v", stats)
	}
}

func TestRecoverExpiredAndQuarantine(t *testing.T) {
	root := t.TempDir()
	queue, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, _, err := queue.Enqueue(notification("recover"), now); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Claim(now, -time.Second); err != nil {
		t.Fatal(err)
	}
	recovered, err := queue.RecoverExpired(now)
	if err != nil || recovered != 1 {
		t.Fatalf("recover: %d %v", recovered, err)
	}

	corrupt := filepath.Join(root, "pending", "corrupt.json")
	if err := os.WriteFile(corrupt, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := queue.List(StatePending); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "dead", "corrupt.json.corrupt")); err != nil {
		t.Fatalf("corrupt item was not quarantined: %v", err)
	}
}

func TestRecoverLeavesActiveLeaseInflight(t *testing.T) {
	queueValue, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, _, err := queueValue.Enqueue(notification("active-lease"), now); err != nil {
		t.Fatal(err)
	}
	if _, err := queueValue.Claim(now, time.Hour); err != nil {
		t.Fatal(err)
	}
	recovered, err := queueValue.RecoverExpired(now.Add(time.Minute))
	if err != nil || recovered != 0 {
		t.Fatalf("recover active lease: recovered=%d err=%v", recovered, err)
	}
	stats, err := queueValue.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Inflight != 1 || stats.Pending != 0 {
		t.Fatalf("unexpected active lease stats: %#v", stats)
	}
}

func TestRecoverCompletesInterruptedAckWithoutResend(t *testing.T) {
	root := t.TempDir()
	queueValue, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, _, err := queueValue.Enqueue(notification("ack-crash"), now); err != nil {
		t.Fatal(err)
	}
	item, err := queueValue.Claim(now, -time.Second)
	if err != nil || item == nil {
		t.Fatalf("claim: %#v %v", item, err)
	}
	item.State = StateSucceeded
	item.LeaseUntil = time.Time{}
	item.UpdatedAt = now.Add(time.Second)
	value, err := json.Marshal(item.Envelope)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "history", item.FileName),
		value,
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	recovered, err := queueValue.RecoverExpired(now.Add(time.Minute))
	if err != nil || recovered != 0 {
		t.Fatalf("recover interrupted ack: recovered=%d err=%v", recovered, err)
	}
	stats, err := queueValue.Stats()
	if err != nil || stats.History != 1 || stats.Inflight != 0 || stats.Pending != 0 {
		t.Fatalf("interrupted ack was resent: %#v err=%v", stats, err)
	}
}

func TestQueueDelayedClaimCleanupAndErrors(t *testing.T) {
	queueValue, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, _, err := queueValue.Enqueue(notification("cleanup"), now); err != nil {
		t.Fatal(err)
	}
	item, err := queueValue.Claim(now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queueValue.Nack(
		item,
		errors.New("wait"),
		now,
		[]time.Duration{time.Hour, time.Hour},
	); err != nil {
		t.Fatal(err)
	}
	if claimed, err := queueValue.Claim(now.Add(time.Minute), time.Minute); err != nil || claimed != nil {
		t.Fatalf("future event was claimed: %#v %v", claimed, err)
	}
	claimed, err := queueValue.Claim(now.Add(2*time.Hour), time.Minute)
	if err != nil || claimed == nil {
		t.Fatalf("delayed event was not claimed: %#v %v", claimed, err)
	}
	if err := queueValue.Ack(claimed, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	removed, err := queueValue.CleanupHistory(now.Add(3 * time.Hour))
	if err != nil || removed != 1 {
		t.Fatalf("cleanup=%d err=%v", removed, err)
	}
	if _, duplicate, err := queueValue.Enqueue(notification("cleanup"), now.Add(4*time.Hour)); err != nil || duplicate {
		t.Fatalf("key was not released after cleanup: duplicate=%v err=%v", duplicate, err)
	}
	if _, err := queueValue.List(State("invalid")); err == nil {
		t.Fatal("expected invalid state error")
	}
	if err := queueValue.Retry("missing", now); err == nil {
		t.Fatal("expected retry missing error")
	}
	if queueValue.Root() == "" {
		t.Fatal("root is empty")
	}
}

func TestQueueCleanupRetentionAndLongError(t *testing.T) {
	queueValue, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, _, err := queueValue.Enqueue(notification("retained"), now); err != nil {
		t.Fatal(err)
	}
	item, err := queueValue.Claim(now, time.Minute)
	if err != nil || item == nil {
		t.Fatalf("claim: %#v %v", item, err)
	}
	longError := errors.New(strings.Repeat("x", 1024))
	state, err := queueValue.Nack(item, longError, now, []time.Duration{time.Second})
	if err != nil || state != StateDead {
		t.Fatalf("nack: state=%s err=%v", state, err)
	}
	dead, err := queueValue.List(StateDead)
	if err != nil {
		t.Fatal(err)
	}
	if len(dead) != 1 || len(dead[0].LastError) != 512 {
		t.Fatalf("unexpected dead event: %#v", dead)
	}
	if err := queueValue.Retry(dead[0].ID, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	item, err = queueValue.Claim(now.Add(time.Second), time.Minute)
	if err != nil || item == nil {
		t.Fatalf("retry claim: %#v %v", item, err)
	}
	if item.Attempts != 0 ||
		item.LastError != "" ||
		item.ManualRetries != 1 ||
		item.LastRetriedAt.IsZero() {
		t.Fatalf("retry fields were not reset: %#v", item.Envelope)
	}
	if err := queueValue.Ack(item, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	removed, err := queueValue.CleanupHistory(now.Add(time.Second))
	if err != nil || removed != 0 {
		t.Fatalf("fresh history was removed: removed=%d err=%v", removed, err)
	}
	history, err := queueValue.List(StateSucceeded)
	if err != nil || len(history) != 1 {
		t.Fatalf("history: %#v %v", history, err)
	}
}

func TestQueueDeadRetentionUsesAgeAndMaximum(t *testing.T) {
	queueValue, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for index, key := range []string{"dead-old", "dead-new-1", "dead-new-2"} {
		eventTime := now.Add(time.Duration(index) * time.Hour)
		if _, _, err := queueValue.Enqueue(notification(key), eventTime); err != nil {
			t.Fatal(err)
		}
		item, err := queueValue.Claim(eventTime, time.Minute)
		if err != nil || item == nil {
			t.Fatalf("claim %s: %#v %v", key, item, err)
		}
		if _, err := queueValue.Nack(
			item,
			errors.New("permanent"),
			eventTime,
			[]time.Duration{},
		); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := queueValue.CleanupDead(now.Add(30*time.Minute), 1)
	if err != nil || removed != 2 {
		t.Fatalf("dead cleanup: removed=%d err=%v", removed, err)
	}
	stats, err := queueValue.Stats()
	if err != nil || stats.Dead != 1 {
		t.Fatalf("dead stats: %#v err=%v", stats, err)
	}
	if _, duplicate, err := queueValue.Enqueue(
		notification("dead-old"),
		now.Add(4*time.Hour),
	); err != nil || duplicate {
		t.Fatalf("removed dead key was retained: duplicate=%v err=%v", duplicate, err)
	}
}

func TestQueueQuarantinesUnsupportedEnvelope(t *testing.T) {
	root := t.TempDir()
	queueValue, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "pending", "unsupported.json")
	if err := os.WriteFile(path, []byte(`{"queueVersion":999,"id":"old"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	items, err := queueValue.List(StatePending)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("unsupported item was returned: %#v", items)
	}
	if _, err := os.Stat(filepath.Join(root, "dead", "unsupported.json.corrupt")); err != nil {
		t.Fatalf("unsupported item was not quarantined: %v", err)
	}
}

func TestQueueRejectsInvalidNotificationAndRoot(t *testing.T) {
	if _, err := Open(""); err == nil {
		t.Fatal("expected empty root error")
	}
	queueValue, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	invalid := notification("invalid")
	invalid.Version = "2"
	if _, _, err := queueValue.Enqueue(invalid, time.Now()); err == nil {
		t.Fatal("expected notification validation error")
	}
}

func TestQueueStorageFailureDoesNotCommitMarkerOrPartialEvent(t *testing.T) {
	root := t.TempDir()
	queueValue, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	tmpDirectory := filepath.Join(root, "tmp")
	if err := os.Remove(tmpDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tmpDirectory, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := queueValue.Enqueue(notification("storage-failure"), time.Now()); err == nil {
		t.Fatal("expected storage failure")
	}
	pending, err := queueValue.List(StatePending)
	if err != nil || len(pending) != 0 {
		t.Fatalf("partial pending event exists: %#v err=%v", pending, err)
	}
	if err := os.Remove(tmpDirectory); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(tmpDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, duplicate, err := queueValue.Enqueue(
		notification("storage-failure"),
		time.Now(),
	); err != nil || duplicate {
		t.Fatalf("idempotency marker survived failed write: duplicate=%v err=%v", duplicate, err)
	}
	if err := os.WriteFile(
		filepath.Join(tmpDirectory, "interrupted-write.tmp"),
		[]byte("{"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	stats, err := queueValue.Stats()
	if err != nil || stats.Pending != 1 {
		t.Fatalf("tmp residue affected queue: %#v err=%v", stats, err)
	}
}

func TestQueueRecoversStaleIdempotencyReservation(t *testing.T) {
	root := t.TempDir()
	queueValue, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(
		root,
		"keys",
		idempotencyFileName("stale-reservation"),
	)
	if err := os.WriteFile(keyPath, []byte("orphan\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Second)
	if err := os.Chtimes(keyPath, old, old); err != nil {
		t.Fatal(err)
	}
	id, duplicate, err := queueValue.Enqueue(
		notification("stale-reservation"),
		time.Now(),
	)
	if err != nil || duplicate || id == "" {
		t.Fatalf("stale reservation recovery: id=%q duplicate=%v err=%v", id, duplicate, err)
	}
}

func TestQueueHelpers(t *testing.T) {
	id, err := randomID()
	if err != nil || len(id) != 32 {
		t.Fatalf("random id=%q err=%v", id, err)
	}
	if idempotencyFileName("same") != idempotencyFileName("same") {
		t.Fatal("idempotency marker name is unstable")
	}
	if truncateError(nil) != "" || truncateError(errors.New("short")) != "short" {
		t.Fatal("unexpected error truncation")
	}
}

func BenchmarkEnqueue(b *testing.B) {
	queue, err := Open(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		value := notification(time.Now().String())
		if _, _, err := queue.Enqueue(value, time.Now()); err != nil {
			b.Fatal(err)
		}
	}
}
