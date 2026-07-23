package event

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNormalizeCodexStop(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	raw := []byte(`{
		"hook_event_name": "Stop",
		"cwd": "C:\\work\\agentbell",
		"session_id": "secret-session",
		"turn_id": "turn-1",
		"last_assistant_message": "must not persist"
	}`)

	notification, err := Normalize("codex", "cli", "host", raw, now)
	if err != nil {
		t.Fatal(err)
	}

	if notification.Event != EventTaskCompleted || notification.Status != StatusCompleted {
		t.Fatalf("unexpected event: %#v", notification)
	}
	if notification.SessionID == "secret-session" || !strings.HasPrefix(notification.SessionID, "sha256:") {
		t.Fatalf("session id was not made opaque: %q", notification.SessionID)
	}
	if notification.Summary != "" || notification.CWD != "" {
		t.Fatalf("metadata-only event persisted private content: %#v", notification)
	}
	if notification.Project != "agentbell" {
		t.Fatalf("unexpected project: %q", notification.Project)
	}
	if err := notification.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestNormalizeMappings(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		event  string
		status string
	}{
		{"failure", `{"hook_event_name":"StopFailure"}`, EventTaskFailed, StatusFailed},
		{"permission", `{"hook_event_name":"PermissionRequest"}`, EventApprovalRequired, StatusAttention},
		{"waiting", `{"hook_event_name":"Notification","notification_type":"idle_prompt"}`, EventAgentWaiting, StatusAttention},
		{"unknown", `{"hook_event_name":"SomethingNew"}`, EventAgentInfo, StatusInfo},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			notification, err := Normalize("codex", "cli", "host", []byte(test.raw), time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if notification.Event != test.event || notification.Status != test.status {
				t.Fatalf("unexpected mapping: %#v", notification)
			}
		})
	}
}

func TestNormalizeRejectsInvalidInput(t *testing.T) {
	if _, err := Normalize("codex", "cli", "host", nil, time.Now()); err == nil {
		t.Fatal("expected empty input error")
	}
	if _, err := Normalize("codex", "cli", "host", []byte("{"), time.Now()); err == nil {
		t.Fatal("expected invalid JSON error")
	}
	if _, err := Normalize("unknown", "cli", "host", []byte("{}"), time.Now()); err == nil {
		t.Fatal("expected unknown adapter error")
	}
	if _, err := Normalize("codex", "unknown", "host", []byte("{}"), time.Now()); err == nil {
		t.Fatal("expected invalid surface error")
	}
}

func TestMetadataOnlyRejectsSensitiveFields(t *testing.T) {
	notification := Notification{
		Version:        Version,
		Source:         "codex",
		Surface:        "cli",
		Runtime:        "host",
		Event:          EventTaskCompleted,
		Status:         StatusCompleted,
		OccurredAt:     time.Now(),
		IdempotencyKey: "key",
		Priority:       PriorityNormal,
		PrivacyLevel:   PrivacyMetadataOnly,
		Summary:        "secret",
	}
	if err := notification.Validate(); err == nil {
		t.Fatal("expected privacy validation error")
	}
}

func TestNormalizeAdditionalMappingsAndTimestamp(t *testing.T) {
	tests := []struct {
		raw   string
		event string
	}{
		{`{"hook_event_name":"Interrupt"}`, EventSessionInterrupted},
		{`{"hook_event_name":"SubagentStop"}`, EventSubagentCompleted},
		{`{"event":"session.idle"}`, EventTaskCompleted},
	}
	for _, test := range tests {
		notification, err := Normalize("claude-code", "desktop", "host", []byte(test.raw), time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if notification.Event != test.event {
			t.Fatalf("%s mapped to %s", test.raw, notification.Event)
		}
	}

	notification, err := Normalize(
		"kimi-code",
		"cli",
		"host",
		[]byte(`{"hook_event_name":"Stop","timestamp":"2026-07-23T10:00:00+08:00","idempotency_key":"provided"}`),
		time.Now(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if notification.OccurredAt.Format(time.RFC3339) != "2026-07-23T02:00:00Z" ||
		notification.IdempotencyKey == "provided" ||
		!strings.HasPrefix(notification.IdempotencyKey, "sha256:") {
		t.Fatalf("timestamp or key not preserved: %#v", notification)
	}
	if _, err := Normalize(
		"codex",
		"cli",
		"host",
		[]byte(`{"hook_event_name":"Stop","timestamp":"bad"}`),
		time.Now(),
	); err == nil {
		t.Fatal("expected timestamp error")
	}
}

func TestValidationMatrix(t *testing.T) {
	valid := Notification{
		Version:        Version,
		Source:         "codex",
		Surface:        "cli",
		Runtime:        "host",
		Event:          EventTaskCompleted,
		Status:         StatusCompleted,
		OccurredAt:     time.Now(),
		IdempotencyKey: "key",
		Priority:       PriorityNormal,
		PrivacyLevel:   PrivacyMetadataOnly,
	}
	mutations := []func(*Notification){
		func(value *Notification) { value.Version = "2" },
		func(value *Notification) { value.Source = "unknown" },
		func(value *Notification) { value.Surface = "unknown" },
		func(value *Notification) { value.Runtime = "unknown" },
		func(value *Notification) { value.Event = "unknown" },
		func(value *Notification) { value.Status = "unknown" },
		func(value *Notification) { value.OccurredAt = time.Time{} },
		func(value *Notification) { value.IdempotencyKey = "" },
		func(value *Notification) { value.Priority = "unknown" },
		func(value *Notification) { value.PrivacyLevel = "unknown" },
		func(value *Notification) {
			value.Summary = strings.Repeat("x", 301)
			value.PrivacyLevel = PrivacySummary
		},
	}
	for index, mutate := range mutations {
		candidate := valid
		mutate(&candidate)
		if err := candidate.Validate(); err == nil {
			t.Fatalf("mutation %d was accepted: %#v", index, candidate)
		}
	}
}

func TestNotificationGoldenFixture(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("unable to locate event test")
	}
	fixturePath := filepath.Join(
		filepath.Dir(currentFile),
		"..",
		"..",
		"testdata",
		"notification-event.golden.json",
	)
	value, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var notification Notification
	if err := json.Unmarshal(value, &notification); err != nil {
		t.Fatal(err)
	}
	if err := notification.Validate(); err != nil {
		t.Fatal(err)
	}
	if notification.Event != EventTaskCompleted ||
		notification.PrivacyLevel != PrivacyMetadataOnly ||
		notification.CWD != "" ||
		notification.Summary != "" {
		t.Fatalf("unexpected golden fixture: %#v", notification)
	}
}
