package queue

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"
)

func TestMigrationFixtureQueueV1(t *testing.T) {
	fixture := readQueueMigrationFixture(t, "queue-v1.json")
	root := t.TempDir()
	queueValue, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	fileName := "00000000000000000001-0123456789abcdef0123456789abcdef.json"
	pendingPath := filepath.Join(root, "pending", fileName)
	if err := os.WriteFile(pendingPath, fixture, 0o600); err != nil {
		t.Fatal(err)
	}

	pending, err := queueValue.List(StatePending)
	if err != nil || len(pending) != 1 {
		t.Fatalf("read Queue v1 fixture: %#v, %v", pending, err)
	}
	legacy := pending[0]
	if legacy.Ledger != nil || legacy.Disposition != "" {
		t.Fatalf("Queue v1 fixture changed before migration: %#v", legacy)
	}
	if err := legacy.Event.Validate(); err != nil {
		t.Fatalf("Queue v1 event is not compatible: %v", err)
	}
	if legacy.Event.CWD != "" || legacy.Event.Summary != "" {
		t.Fatalf("Queue v1 fixture exposed private event content: %#v", legacy.Event)
	}

	now := time.Date(2026, 1, 2, 3, 5, 0, 0, time.UTC)
	item, err := queueValue.Claim(now, time.Minute)
	if err != nil || item == nil {
		t.Fatalf("claim Queue v1 fixture: %#v, %v", item, err)
	}
	targets := []DeliveryTarget{{
		ChannelID:  "fixture-channel",
		TemplateID: "fixture-template",
	}}
	ready, err := queueValue.ResolveTargets(item, targets, now)
	if err != nil {
		t.Fatalf("migrate Queue v1 delivery ledger: %v", err)
	}
	if !reflect.DeepEqual(ready, targets) {
		t.Fatalf("Queue v1 migration resolved unexpected targets: %#v", ready)
	}
	expectedLedger := []DeliveryLedgerEntry{{
		ChannelID:  "fixture-channel",
		TemplateID: "fixture-template",
		State:      DeliveryPending,
	}}
	if !reflect.DeepEqual(item.Ledger, expectedLedger) ||
		item.Disposition != DispositionPending ||
		item.Attempts != legacy.Attempts ||
		item.Event != legacy.Event {
		t.Fatalf("Queue v1 migration changed unrelated state: %#v", item.Envelope)
	}

	persisted, err := os.ReadFile(filepath.Join(root, "inflight", fileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, privateField := range [][]byte{
		[]byte(`"cwd"`),
		[]byte(`"summary"`),
	} {
		if bytes.Contains(bytes.ToLower(persisted), privateField) {
			t.Fatalf("migrated Queue v1 persisted private field %s", privateField)
		}
	}
}

func readQueueMigrationFixture(t *testing.T, name string) []byte {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve migration fixture source")
	}
	value, err := os.ReadFile(filepath.Join(
		filepath.Dir(source),
		"..",
		"..",
		"testdata",
		"migrations",
		name,
	))
	if err != nil {
		t.Fatal(err)
	}
	return value
}
