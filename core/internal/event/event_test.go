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

func TestNormalizeM15ProductMappings(t *testing.T) {
	now := time.Date(2026, 7, 26, 13, 0, 0, 0, time.UTC)
	qoderWork, err := Normalize(
		"qoder-work",
		"desktop",
		"host",
		[]byte(`{"hook_event_name":"Stop","session_id":"qw-session"}`),
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if qoderWork.Source != "qoder-work" ||
		qoderWork.Event != EventTaskCompleted ||
		qoderWork.Surface != "desktop" {
		t.Fatalf("unexpected QoderWork notification: %#v", qoderWork)
	}

	tests := []struct {
		name             string
		notificationType string
		event            string
		status           string
	}{
		{"completed", "idle_prompt", EventTaskCompleted, StatusCompleted},
		{"approval", "permission_prompt", EventApprovalRequired, StatusAttention},
		{"unknown", "document_review", EventAgentInfo, StatusInfo},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := []byte(`{
				"hook_event_name":"Notification",
				"notification_type":"` + test.notificationType + `",
				"session_id":"trae-session",
				"tool_use_id":"tool-1"
			}`)
			notification, err := Normalize("trae", "ide", "host", raw, now)
			if err != nil {
				t.Fatal(err)
			}
			if notification.Event != test.event ||
				notification.Status != test.status ||
				notification.Source != "trae" {
				t.Fatalf("unexpected TRAE mapping: %#v", notification)
			}
		})
	}

	claude, err := Normalize(
		"claude-code",
		"desktop",
		"host",
		[]byte(`{"hook_event_name":"Notification","notification_type":"idle_prompt"}`),
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if claude.Event != EventAgentWaiting {
		t.Fatalf("TRAE product semantics leaked into Claude: %#v", claude)
	}
}

func TestCodexPermissionNotificationRequiresExplicitUserReviewer(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{
			name: "current ambiguous payload",
			raw:  `{"hook_event_name":"PermissionRequest","permission_mode":"default"}`,
			want: false,
		},
		{
			name: "auto review",
			raw:  `{"hook_event_name":"PermissionRequest","approvals_reviewer":"auto_review"}`,
			want: false,
		},
		{
			name: "future nested user signal",
			raw: `{
				"hook_event_name":"PermissionRequest",
				"approval_context":{"approvals_reviewer":"user"}
			}`,
			want: true,
		},
		{
			name: "future top level user signal",
			raw:  `{"hook_event_name":"PermissionRequest","approvals_reviewer":"user"}`,
			want: true,
		},
		{
			name: "completion",
			raw:  `{"hook_event_name":"Stop"}`,
			want: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ShouldNotify("codex", []byte(test.raw))
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("ShouldNotify()=%v want=%v", got, test.want)
			}
		})
	}
	deliver, err := ShouldNotify(
		"claude-code",
		[]byte(`{"hook_event_name":"PermissionRequest"}`),
	)
	if err != nil || !deliver {
		t.Fatalf("non-Codex approval was suppressed: deliver=%v err=%v", deliver, err)
	}
	if _, err := ShouldNotify("codex", []byte("{")); err == nil {
		t.Fatal("expected malformed Codex input error")
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
	if _, err := Normalize(
		"kimi-code",
		"cli",
		"host",
		[]byte(`{"hook_event_name":"PermissionRequest","turn_id":true}`),
		time.Now(),
	); err == nil {
		t.Fatal("expected non-scalar identifier error")
	}
}

func TestNormalizeKimiNumericAndToolIdentifiers(t *testing.T) {
	now := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
	first, err := Normalize(
		"kimi-code",
		"cli",
		"host",
		[]byte(`{
			"hook_event_name":"PermissionRequest",
			"session_id":"session",
			"turn_id":17,
			"tool_call_id":42
		}`),
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Normalize(
		"kimi-code",
		"cli",
		"host",
		[]byte(`{
			"hook_event_name":"PermissionRequest",
			"session_id":"session",
			"turn_id":17,
			"tool_call_id":43
		}`),
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if first.TurnID == "" || !strings.HasPrefix(first.TurnID, "sha256:") {
		t.Fatalf("numeric turn id was not normalized: %#v", first)
	}
	if first.IdempotencyKey == second.IdempotencyKey {
		t.Fatal("different tool calls must not collapse to one notification")
	}
}

func TestNormalizeKimiSessionOnlyStopsDoNotCollapseAcrossTurns(t *testing.T) {
	raw := []byte(`{"hook_event_name":"Stop","session_id":"same-session"}`)
	firstTime := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
	first, err := Normalize("kimi-code", "cli", "host", raw, firstTime)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := Normalize("kimi-code", "cli", "host", raw, firstTime)
	if err != nil {
		t.Fatal(err)
	}
	nextTurn, err := Normalize("kimi-code", "cli", "host", raw, firstTime.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if first.IdempotencyKey != retry.IdempotencyKey {
		t.Fatal("the same occurrence must remain deterministic")
	}
	if first.IdempotencyKey == nextTurn.IdempotencyKey {
		t.Fatal("later session-only Stop events must not be permanently deduplicated")
	}
}

func TestNormalizeClaudePromptIDProvidesStableTurnIdentity(t *testing.T) {
	raw := []byte(`{
		"hook_event_name":"Stop",
		"session_id":"claude-session",
		"prompt_id":"prompt-42"
	}`)
	first, err := Normalize(
		"claude-code",
		"cli",
		"host",
		raw,
		time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := Normalize(
		"claude-code",
		"cli",
		"host",
		raw,
		time.Date(2026, 7, 25, 10, 0, 5, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	if first.IdempotencyKey != retry.IdempotencyKey ||
		first.TurnID == "" ||
		!strings.HasPrefix(first.TurnID, "sha256:") {
		t.Fatalf("Claude prompt identity was not stable: %#v %#v", first, retry)
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

func TestKnownNotificationDimensions(t *testing.T) {
	tests := []struct {
		name    string
		known   string
		unknown string
		check   func(string) bool
	}{
		{name: "source", known: "codex", unknown: "future", check: IsKnownSource},
		{name: "surface", known: "desktop", unknown: "terminal", check: IsKnownSurface},
		{name: "runtime", known: "wsl", unknown: "vm", check: IsKnownRuntime},
		{name: "event", known: EventTaskCompleted, unknown: "task.future", check: IsKnownEvent},
		{name: "status", known: StatusAttention, unknown: "pending", check: IsKnownStatus},
		{name: "priority", known: "urgent", unknown: "critical", check: IsKnownPriority},
		{name: "privacy", known: PrivacyMetadataOnly, unknown: "secret", check: IsKnownPrivacy},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !test.check(test.known) {
				t.Fatalf("%q should be known", test.known)
			}
			if test.check(test.unknown) {
				t.Fatalf("%q should be unknown", test.unknown)
			}
		})
	}

	events := KnownEvents()
	if len(events) != 7 || !IsKnownEvent(events[0]) {
		t.Fatalf("known events are incomplete: %#v", events)
	}
	events[0] = "mutated"
	if !IsKnownEvent(EventTaskCompleted) {
		t.Fatal("KnownEvents exposed mutable authoritative state")
	}
}
