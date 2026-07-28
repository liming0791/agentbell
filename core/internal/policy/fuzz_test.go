package policy

import (
	"bytes"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/liming0791/agentbell/core/internal/settings"
)

func FuzzTemplateRender(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte("{{sourceName}} {{event}} {{status}} {{project}} " +
			"{{priority}} {{occurredAt}} {{runtime}} {{surface}}"),
		[]byte("literal text"),
		[]byte("{{summary}}"),
		[]byte("{{event"),
		bytes.Repeat([]byte("x"), MaximumTemplateBytes+1),
		{0xff, 0xfe, '{', '{'},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		body := string(raw)
		input := TemplateInput{
			SourceName: "Codex",
			Event:      "task.completed",
			Status:     "completed",
			Project:    "agentbell",
			Priority:   "normal",
			OccurredAt: time.Date(2026, 7, 27, 1, 2, 3, 4, time.UTC),
			Runtime:    "host",
			Surface:    "cli",
		}
		template := settings.Template{ID: "fuzz", Body: body}
		first, err := Render(template, input)
		if err != nil {
			return
		}
		if err := template.Validate(); err != nil {
			t.Fatal("successful template render violated template Validate")
		}
		if !utf8.ValidString(first) || len(first) > MaximumOutputBytes {
			t.Fatal("successful template render violated output bounds")
		}
		second, err := Render(template, input)
		if err != nil || first != second {
			t.Fatal("template rendering was not deterministic")
		}
		privateMarker := []byte("PRIVATE-SUMMARY-SENTINEL")
		if !bytes.Contains(raw, privateMarker) &&
			bytes.Contains([]byte(first), privateMarker) {
			t.Fatal("template rendering introduced a private field")
		}
	})
}
