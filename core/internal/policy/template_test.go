package policy

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/liming0791/agentbell/core/internal/event"
	"github.com/liming0791/agentbell/core/internal/settings"
)

func TestRenderUsesOnlyMetadataFields(t *testing.T) {
	template := settings.Template{
		ID: "all",
		Body: "{{sourceName}}|{{event}}|{{status}}|{{project}}|" +
			"{{priority}}|{{occurredAt}}|{{runtime}}|{{surface}}",
	}
	input := TemplateInput{
		SourceName: "Codex",
		Event:      event.EventTaskFailed,
		Status:     event.StatusFailed,
		Project:    "agentbell",
		Priority:   "high",
		OccurredAt: time.Date(2026, 7, 27, 9, 30, 0, 123, time.FixedZone("SGT", 8*60*60)),
		Runtime:    "host",
		Surface:    "cli",
	}
	rendered, err := Render(template, input)
	if err != nil {
		t.Fatal(err)
	}
	expected := "Codex|task.failed|failed|agentbell|high|" +
		"2026-07-27T01:30:00.000000123Z|host|cli"
	if rendered != expected {
		t.Fatalf("rendered = %q, want %q", rendered, expected)
	}

	preview, err := Preview(template.Body, input)
	if err != nil || preview != rendered {
		t.Fatalf("preview = %q err=%v, want render output", preview, err)
	}
}

func TestTemplateReplacementIsSinglePass(t *testing.T) {
	rendered, err := Preview(
		"project={{project}} event={{event}}",
		TemplateInput{
			Project:    "{{event}}",
			Event:      "task.failed",
			OccurredAt: time.Now(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if rendered != "project={{event}} event=task.failed" {
		t.Fatalf("replacement recursively expanded input: %q", rendered)
	}
}

func TestInputFromNotificationRequiresMetadataOnly(t *testing.T) {
	notification := testNotification()
	input, err := InputFromNotification(notification, "Codex")
	if err != nil {
		t.Fatal(err)
	}
	if input.Project != notification.Project ||
		input.SourceName != "Codex" ||
		input.Event != notification.Event {
		t.Fatalf("input mismatch: %#v", input)
	}

	notification.PrivacyLevel = event.PrivacySummary
	notification.Summary = "private"
	if _, err := InputFromNotification(notification, "Codex"); err == nil ||
		!strings.Contains(err.Error(), "metadata-only") {
		t.Fatalf("summary input was accepted: %v", err)
	}
}

func TestRenderRejectsUnsafeOrOversizedInputs(t *testing.T) {
	tests := []struct {
		name     string
		template settings.Template
		input    TemplateInput
		want     string
	}{
		{
			name:     "unknown placeholder",
			template: settings.Template{ID: "bad", Body: "{{summary}}"},
			input:    TemplateInput{OccurredAt: time.Now()},
			want:     "placeholder",
		},
		{
			name: "template over 4KiB",
			template: settings.Template{
				ID: "bad", Body: strings.Repeat("x", MaximumTemplateBytes+1),
			},
			input: TemplateInput{OccurredAt: time.Now()},
			want:  "4096",
		},
		{
			name: "invalid template UTF-8",
			template: settings.Template{
				ID: "bad", Body: string([]byte{0xff}),
			},
			input: TemplateInput{OccurredAt: time.Now()},
			want:  "UTF-8",
		},
		{
			name:     "invalid input UTF-8",
			template: settings.Template{ID: "bad", Body: "{{project}}"},
			input: TemplateInput{
				Project: string([]byte{0xff}), OccurredAt: time.Now(),
			},
			want: "UTF-8",
		},
		{
			name: "output over 16KiB",
			template: settings.Template{
				ID: "large", Body: strings.Repeat("{{project}}", 300),
			},
			input: TemplateInput{
				Project: strings.Repeat("界", 100), OccurredAt: time.Now(),
			},
			want: "16384",
		},
		{
			name:     "missing occurredAt",
			template: settings.Template{ID: "bad", Body: "{{event}}"},
			input:    TemplateInput{Event: event.EventTaskFailed},
			want:     "occurredAt",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Render(test.template, test.input); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("Render error = %v, want %q", err, test.want)
			}
		})
	}
	if !utf8.ValidString("界") {
		t.Fatal("test runtime does not support valid UTF-8")
	}
}
