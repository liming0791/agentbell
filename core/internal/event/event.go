package event

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
)

const (
	Version = "1"

	EventTaskCompleted      = "task.completed"
	EventTaskFailed         = "task.failed"
	EventAgentWaiting       = "agent.waiting"
	EventApprovalRequired   = "approval.required"
	EventSessionInterrupted = "session.interrupted"
	EventSubagentCompleted  = "subagent.completed"
	EventAgentInfo          = "agent.info"

	StatusCompleted = "completed"
	StatusFailed    = "failed"
	StatusAttention = "attention"
	StatusInfo      = "info"

	PriorityNormal = "normal"

	PrivacyMetadataOnly = "metadata-only"
	PrivacySummary      = "summary"
	PrivacyFull         = "full"
)

var (
	allowedSources = setOf(
		"codex", "claude", "opencode", "kimi", "qoder",
		"zcode", "workbuddy", "trae", "kimi-work",
	)
	allowedSurfaces = setOf("cli", "tui", "ide", "desktop", "jetbrains", "cloud")
	allowedRuntimes = setOf("host", "wsl", "container", "ssh", "vendor-cloud")
	allowedEvents   = setOf(
		EventTaskCompleted,
		EventTaskFailed,
		EventAgentWaiting,
		EventApprovalRequired,
		EventSessionInterrupted,
		EventSubagentCompleted,
		EventAgentInfo,
	)
	allowedStatuses   = setOf(StatusCompleted, StatusFailed, StatusAttention, StatusInfo)
	allowedPriorities = setOf("normal", "high", "urgent")
	allowedPrivacy    = setOf(PrivacyMetadataOnly, PrivacySummary, PrivacyFull)
)

type Notification struct {
	Version        string    `json:"version"`
	Source         string    `json:"source"`
	Surface        string    `json:"surface"`
	Runtime        string    `json:"runtime"`
	Event          string    `json:"event"`
	Status         string    `json:"status"`
	OccurredAt     time.Time `json:"occurredAt"`
	SessionID      string    `json:"sessionId,omitempty"`
	TaskID         string    `json:"taskId,omitempty"`
	TurnID         string    `json:"turnId,omitempty"`
	IdempotencyKey string    `json:"idempotencyKey"`
	Priority       string    `json:"priority"`
	PrivacyLevel   string    `json:"privacyLevel"`
	Project        string    `json:"project,omitempty"`
	CWD            string    `json:"cwd,omitempty"`
	Summary        string    `json:"summary,omitempty"`
}

func (notification Notification) Validate() error {
	if notification.Version != Version {
		return fmt.Errorf("unsupported notification version %q", notification.Version)
	}
	if !allowedSources[notification.Source] {
		return fmt.Errorf("unsupported source %q", notification.Source)
	}
	if !allowedSurfaces[notification.Surface] {
		return fmt.Errorf("unsupported surface %q", notification.Surface)
	}
	if !allowedRuntimes[notification.Runtime] {
		return fmt.Errorf("unsupported runtime %q", notification.Runtime)
	}
	if !allowedEvents[notification.Event] {
		return fmt.Errorf("unsupported event %q", notification.Event)
	}
	if !allowedStatuses[notification.Status] {
		return fmt.Errorf("unsupported status %q", notification.Status)
	}
	if notification.OccurredAt.IsZero() {
		return errors.New("occurredAt is required")
	}
	if notification.IdempotencyKey == "" {
		return errors.New("idempotencyKey is required")
	}
	if !allowedPriorities[notification.Priority] {
		return fmt.Errorf("unsupported priority %q", notification.Priority)
	}
	if !allowedPrivacy[notification.PrivacyLevel] {
		return fmt.Errorf("unsupported privacy level %q", notification.PrivacyLevel)
	}
	if len(notification.Summary) > 300 {
		return errors.New("summary exceeds 300 characters")
	}
	if notification.PrivacyLevel == PrivacyMetadataOnly &&
		(notification.CWD != "" || notification.Summary != "") {
		return errors.New("metadata-only notifications cannot include cwd or summary")
	}
	return nil
}

type rawEvent struct {
	HookEventName        string `json:"hook_event_name"`
	Event                string `json:"event"`
	CWD                  string `json:"cwd"`
	SessionID            string `json:"session_id"`
	ThreadID             string `json:"thread_id"`
	TaskID               string `json:"task_id"`
	TurnID               string `json:"turn_id"`
	IdempotencyKey       string `json:"idempotency_key"`
	LastAssistantMessage string `json:"last_assistant_message"`
	Message              string `json:"message"`
	Reason               string `json:"reason"`
	NotificationType     string `json:"notification_type"`
	OccurredAt           string `json:"occurred_at"`
	Timestamp            string `json:"timestamp"`
}

func Normalize(adapterID, surface, runtimeName string, raw []byte, now time.Time) (Notification, error) {
	if len(raw) == 0 {
		return Notification{}, errors.New("hook input is empty")
	}

	var payload rawEvent
	if err := json.Unmarshal(raw, &payload); err != nil {
		return Notification{}, fmt.Errorf("invalid hook JSON: %w", err)
	}

	source, ok := sourceForAdapter(adapterID)
	if !ok {
		return Notification{}, fmt.Errorf("unsupported adapter %q", adapterID)
	}

	rawName := payload.HookEventName
	if rawName == "" {
		rawName = payload.Event
	}
	canonical, status := canonicalEvent(rawName, payload.NotificationType)

	occurredAt := now.UTC()
	for _, candidate := range []string{payload.OccurredAt, payload.Timestamp} {
		if candidate == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339Nano, candidate)
		if err != nil {
			return Notification{}, fmt.Errorf("invalid event timestamp: %w", err)
		}
		occurredAt = parsed.UTC()
		break
	}

	sessionID := firstNonEmpty(payload.SessionID, payload.ThreadID)
	keyMaterial := strings.Join([]string{
		source,
		surface,
		runtimeName,
		sessionID,
		payload.TaskID,
		payload.TurnID,
		canonical,
	}, "\x00")
	if sessionID == "" && payload.TaskID == "" && payload.TurnID == "" {
		keyMaterial += "\x00" + string(raw)
	}

	idempotencyKey := payload.IdempotencyKey
	if idempotencyKey != "" {
		keyMaterial = "provided\x00" + idempotencyKey
	}
	idempotencyKey = "sha256:" + sha256Hex([]byte(keyMaterial))

	project := ""
	if payload.CWD != "" {
		normalizedPath := strings.ReplaceAll(payload.CWD, `\`, "/")
		project = path.Base(path.Clean(normalizedPath))
		if project == "." || project == "/" {
			project = ""
		}
	}

	notification := Notification{
		Version:        Version,
		Source:         source,
		Surface:        surface,
		Runtime:        runtimeName,
		Event:          canonical,
		Status:         status,
		OccurredAt:     occurredAt,
		SessionID:      opaqueIdentifier(sessionID),
		TaskID:         opaqueIdentifier(payload.TaskID),
		TurnID:         opaqueIdentifier(payload.TurnID),
		IdempotencyKey: idempotencyKey,
		Priority:       PriorityNormal,
		PrivacyLevel:   PrivacyMetadataOnly,
		Project:        project,
	}

	if err := notification.Validate(); err != nil {
		return Notification{}, err
	}
	return notification, nil
}

func sourceForAdapter(adapterID string) (string, bool) {
	sources := map[string]string{
		"codex":       "codex",
		"claude":      "claude",
		"claude-code": "claude",
		"opencode":    "opencode",
		"kimi":        "kimi",
		"kimi-code":   "kimi",
		"qoder":       "qoder",
		"zcode":       "zcode",
		"workbuddy":   "workbuddy",
		"trae":        "trae",
		"kimi-work":   "kimi-work",
	}
	source, ok := sources[adapterID]
	return source, ok
}

func canonicalEvent(rawName, notificationType string) (string, string) {
	switch strings.ToLower(rawName) {
	case "stop", "completed", "session.idle", "task.completed":
		return EventTaskCompleted, StatusCompleted
	case "stopfailure", "posttoolusefailure", "session.error", "failure", "error", "task.failed":
		return EventTaskFailed, StatusFailed
	case "permissionrequest", "permission.asked", "approval.required":
		return EventApprovalRequired, StatusAttention
	case "interrupt", "session.interrupted":
		return EventSessionInterrupted, StatusAttention
	case "subagentstop", "subagent.completed":
		return EventSubagentCompleted, StatusCompleted
	case "agent.waiting":
		return EventAgentWaiting, StatusAttention
	case "notification":
		if strings.EqualFold(notificationType, "idle_prompt") ||
			strings.EqualFold(notificationType, "agent_needs_input") {
			return EventAgentWaiting, StatusAttention
		}
	}
	return EventAgentInfo, StatusInfo
}

func opaqueIdentifier(value string) string {
	if value == "" {
		return ""
	}
	return "sha256:" + sha256Hex([]byte(value))[:16]
}

func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func setOf(values ...string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}
