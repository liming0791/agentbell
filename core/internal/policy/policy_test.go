package policy

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/liming0791/agentbell/core/internal/event"
	"github.com/liming0791/agentbell/core/internal/settings"
)

func testSettings() settings.Settings {
	events := make(settings.EventSwitches)
	for _, name := range event.KnownEvents() {
		events[name] = true
	}
	return settings.Settings{
		Version:         settings.Version,
		MinCoreVersion:  "0.3.0",
		Events:          events,
		DefaultTemplate: "standard",
		Templates: []settings.Template{
			{ID: "standard", Body: "{{sourceName}} {{event}}"},
			{ID: "terse", Body: "{{status}}"},
		},
		QuietHours: settings.QuietHours{Enabled: false},
		Policies:   []settings.Policy{},
	}
}

func testNotification() event.Notification {
	return event.Notification{
		Version:        event.Version,
		Source:         "codex",
		Surface:        "cli",
		Runtime:        "host",
		Event:          event.EventTaskFailed,
		Status:         event.StatusFailed,
		OccurredAt:     time.Date(2026, 7, 27, 9, 30, 0, 0, time.UTC),
		IdempotencyKey: "sha256:test",
		Priority:       "high",
		PrivacyLevel:   event.PrivacyMetadataOnly,
		Project:        "agentbell",
	}
}

func TestEvaluateUsesFirstMatchingPolicyAndAllSelectors(t *testing.T) {
	settingsValue := testSettings()
	disabled := false
	enabled := true
	settingsValue.Policies = []settings.Policy{
		{
			ID: "first",
			Match: settings.PolicyMatch{
				Events:     []string{event.EventTaskFailed},
				Sources:    []string{"codex"},
				Surfaces:   []string{"cli"},
				Runtimes:   []string{"host"},
				Priorities: []string{"high"},
				Projects:   []string{"agentbell"},
			},
			Action: settings.PolicyAction{
				Enabled:    &disabled,
				ChannelIDs: []string{"incident", "audit"},
				TemplateID: "terse",
			},
		},
		{
			ID:     "second",
			Match:  settings.PolicyMatch{Events: []string{event.EventTaskFailed}},
			Action: settings.PolicyAction{Enabled: &enabled, ChannelIDs: []string{"wrong"}},
		},
	}

	decision, err := Evaluate(
		settingsValue,
		testNotification(),
		Defaults{ChannelIDs: []string{"default"}, TemplateID: "standard"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Matched ||
		decision.PolicyID != "first" ||
		decision.Enabled ||
		strings.Join(decision.ChannelIDs, ",") != "incident,audit" ||
		decision.TemplateID != "terse" {
		t.Fatalf("unexpected decision: %#v", decision)
	}
}

func BenchmarkFanOutPolicyEvaluation(b *testing.B) {
	settingsValue := testSettings()
	enabled := true
	channelIDs := make([]string, 32)
	for index := range channelIDs {
		channelIDs[index] = fmt.Sprintf("channel-%02d", index)
	}
	settingsValue.Policies = []settings.Policy{{
		ID: "fan-out",
		Match: settings.PolicyMatch{
			Events:  []string{event.EventTaskFailed},
			Sources: []string{"codex"},
		},
		Action: settings.PolicyAction{
			Enabled:    &enabled,
			ChannelIDs: channelIDs,
			TemplateID: "terse",
		},
	}}
	notification := testNotification()
	defaults := Defaults{
		ChannelIDs: []string{"default"},
		TemplateID: "standard",
	}
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		decision, err := Evaluate(settingsValue, notification, defaults)
		if err != nil {
			b.Fatal(err)
		}
		if len(decision.ChannelIDs) != len(channelIDs) {
			b.Fatal("fan-out decision lost channels")
		}
	}
}

func TestEvaluateSelectorMismatchAndDefaults(t *testing.T) {
	base := testNotification()
	match := settings.PolicyMatch{
		Events:     []string{base.Event},
		Sources:    []string{base.Source},
		Surfaces:   []string{base.Surface},
		Runtimes:   []string{base.Runtime},
		Priorities: []string{base.Priority},
		Projects:   []string{base.Project},
	}
	tests := []struct {
		name   string
		mutate func(*event.Notification)
	}{
		{name: "event", mutate: func(value *event.Notification) {
			value.Event = event.EventTaskCompleted
			value.Status = event.StatusCompleted
		}},
		{name: "source", mutate: func(value *event.Notification) { value.Source = "claude" }},
		{name: "surface", mutate: func(value *event.Notification) { value.Surface = "desktop" }},
		{name: "runtime", mutate: func(value *event.Notification) { value.Runtime = "wsl" }},
		{name: "priority", mutate: func(value *event.Notification) { value.Priority = "urgent" }},
		{name: "project", mutate: func(value *event.Notification) { value.Project = "other" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			settingsValue := testSettings()
			disabled := false
			settingsValue.Policies = []settings.Policy{{
				ID: "specific", Match: match,
				Action: settings.PolicyAction{
					Enabled: &disabled, ChannelIDs: []string{"specific"},
				},
			}}
			notification := base
			test.mutate(&notification)
			decision, err := Evaluate(
				settingsValue,
				notification,
				Defaults{ChannelIDs: []string{"default"}, TemplateID: "standard"},
			)
			if err != nil {
				t.Fatal(err)
			}
			if decision.Matched ||
				!decision.Enabled ||
				len(decision.ChannelIDs) != 1 ||
				decision.ChannelIDs[0] != "default" ||
				decision.TemplateID != "standard" {
				t.Fatalf("mismatch did not use defaults: %#v", decision)
			}
		})
	}
}

func TestEvaluateEventSwitchAndPartialActions(t *testing.T) {
	settingsValue := testSettings()
	settingsValue.Events[event.EventTaskFailed] = false

	decision, err := Evaluate(
		settingsValue,
		testNotification(),
		Defaults{ChannelIDs: []string{"default"}, TemplateID: "standard"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Enabled {
		t.Fatalf("disabled event was enabled: %#v", decision)
	}

	enabled := true
	settingsValue.Policies = []settings.Policy{{
		ID:     "override",
		Match:  settings.PolicyMatch{Sources: []string{"codex"}},
		Action: settings.PolicyAction{Enabled: &enabled, TemplateID: "terse"},
	}}
	decision, err = Evaluate(
		settingsValue,
		testNotification(),
		Defaults{ChannelIDs: []string{"default"}, TemplateID: "standard"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Enabled ||
		decision.ChannelIDs[0] != "default" ||
		decision.TemplateID != "terse" {
		t.Fatalf("partial action did not inherit defaults: %#v", decision)
	}
}

func TestEvaluateRejectsInvalidInputsAndCopiesDefaults(t *testing.T) {
	settingsValue := testSettings()
	if _, err := Evaluate(settingsValue, testNotification(), Defaults{}); err == nil {
		t.Fatal("empty defaults were accepted")
	}

	broken := settingsValue
	broken.Version = 99
	if _, err := Evaluate(
		broken,
		testNotification(),
		Defaults{ChannelIDs: []string{"default"}, TemplateID: "standard"},
	); err == nil {
		t.Fatal("invalid settings were accepted")
	}

	invalidNotification := testNotification()
	invalidNotification.Priority = "future"
	if _, err := Evaluate(
		settingsValue,
		invalidNotification,
		Defaults{ChannelIDs: []string{"default"}, TemplateID: "standard"},
	); err == nil {
		t.Fatal("invalid notification was accepted")
	}

	channels := []string{"default"}
	decision, err := Evaluate(
		settingsValue,
		testNotification(),
		Defaults{ChannelIDs: channels, TemplateID: "standard"},
	)
	if err != nil {
		t.Fatal(err)
	}
	channels[0] = "mutated"
	if decision.ChannelIDs[0] != "default" {
		t.Fatal("decision aliases caller defaults")
	}
}
