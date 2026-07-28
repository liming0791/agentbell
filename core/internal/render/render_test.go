package render

import (
	"strings"
	"testing"
	"time"

	"github.com/liming0791/agentbell/core/internal/config"
	"github.com/liming0791/agentbell/core/internal/event"
)

func TestText(t *testing.T) {
	notification := event.Notification{
		Version:      event.Version,
		Source:       "codex",
		Event:        event.EventTaskCompleted,
		Status:       event.StatusCompleted,
		OccurredAt:   time.Now(),
		Project:      "agentbell",
		Summary:      "done",
		PrivacyLevel: event.PrivacySummary,
	}
	text := Text(notification, config.Config{
		Notifications: config.Notifications{IncludeSummary: true},
	})
	for _, expected := range []string{"Codex", "任务已完成", "agentbell", "done"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("%q missing from %q", expected, text)
		}
	}
}

func TestTextOmitsSummaryAndUsesFallbackSource(t *testing.T) {
	text := Text(event.Notification{
		Source: "future", Event: event.EventAgentInfo, Status: event.StatusInfo, Summary: "secret",
	}, config.Config{})
	if strings.Contains(text, "secret") || !strings.Contains(text, "future") {
		t.Fatalf("unexpected text: %q", text)
	}
}

func TestTextUsesM15ProductNames(t *testing.T) {
	for source, expected := range map[string]string{
		"qoder-work": "QoderWork",
		"trae":       "TRAE",
	} {
		text := Text(event.Notification{
			Source: source,
			Event:  event.EventTaskCompleted,
			Status: event.StatusCompleted,
		}, config.Config{})
		if !strings.Contains(text, expected) {
			t.Fatalf("%s display name missing from %q", source, text)
		}
	}
}
