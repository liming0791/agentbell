package policy

import (
	"testing"
	"time"

	"github.com/liming0791/agentbell/core/internal/event"
	"github.com/liming0791/agentbell/core/internal/settings"
)

func quietSettings(action string) settings.QuietHours {
	return settings.QuietHours{
		Enabled:  true,
		Timezone: "Asia/Singapore",
		Action:   action,
		Intervals: []settings.QuietInterval{{
			Days: []string{"mon"}, Start: "22:00", End: "08:00",
		}},
	}
}

func TestQuietHoursCrossMidnightAndInjectedTime(t *testing.T) {
	location, err := time.LoadLocation("Asia/Singapore")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		now        time.Time
		active     bool
		deferUntil time.Time
	}{
		{
			name:       "start day",
			now:        time.Date(2026, 7, 27, 23, 0, 0, 0, location),
			active:     true,
			deferUntil: time.Date(2026, 7, 28, 8, 0, 0, 0, location),
		},
		{
			name:       "following day",
			now:        time.Date(2026, 7, 28, 1, 0, 0, 0, location),
			active:     true,
			deferUntil: time.Date(2026, 7, 28, 8, 0, 0, 0, location),
		},
		{
			name:   "end is exclusive",
			now:    time.Date(2026, 7, 28, 8, 0, 0, 0, location),
			active: false,
		},
		{
			name:   "wrong day",
			now:    time.Date(2026, 7, 29, 1, 0, 0, 0, location),
			active: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evaluator := QuietEvaluator{Now: func() time.Time { return test.now }}
			decision, err := evaluator.Evaluate(
				quietSettings("defer"),
				testNotification(),
				QuietOptions{},
			)
			if err != nil {
				t.Fatal(err)
			}
			if decision.Active != test.active {
				t.Fatalf("active = %v, want %v: %#v", decision.Active, test.active, decision)
			}
			if test.active &&
				!decision.DeferUntil.Equal(test.deferUntil.UTC()) {
				t.Fatalf(
					"deferUntil = %s, want %s",
					decision.DeferUntil,
					test.deferUntil.UTC(),
				)
			}
		})
	}
}

func TestQuietHoursUrgentAndEventBypass(t *testing.T) {
	now := time.Date(2026, 7, 27, 23, 0, 0, 0, time.FixedZone("SGT", 8*60*60))
	evaluator := QuietEvaluator{Now: func() time.Time { return now }}
	notification := testNotification()
	notification.Priority = "urgent"

	decision, err := evaluator.Evaluate(quietSettings("defer"), notification, QuietOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Active || decision.Reason != ReasonUrgentBypass {
		t.Fatalf("urgent did not bypass by default: %#v", decision)
	}

	disabled := false
	decision, err = evaluator.Evaluate(
		quietSettings("defer"),
		notification,
		QuietOptions{UrgentBypass: &disabled},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Active || decision.Action != ActionDefer {
		t.Fatalf("urgent bypass could not be disabled: %#v", decision)
	}

	quiet := quietSettings("defer")
	quiet.BypassEvents = []string{event.EventTaskFailed}
	notification.Priority = "high"
	decision, err = evaluator.Evaluate(quiet, notification, QuietOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Active || decision.Reason != ReasonEventBypass {
		t.Fatalf("event bypass was ignored: %#v", decision)
	}
}

func TestQuietHoursDropMustBeExplicit(t *testing.T) {
	now := time.Date(2026, 7, 27, 23, 0, 0, 0, time.FixedZone("SGT", 8*60*60))
	evaluator := QuietEvaluator{Now: func() time.Time { return now }}

	decision, err := evaluator.Evaluate(
		quietSettings("drop"),
		testNotification(),
		QuietOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Active || decision.Action != ActionDrop || !decision.DeferUntil.IsZero() {
		t.Fatalf("explicit drop decision is wrong: %#v", decision)
	}

	quiet := quietSettings("")
	if _, err := evaluator.Evaluate(quiet, testNotification(), QuietOptions{}); err == nil {
		t.Fatal("empty action silently became drop")
	}

	quiet.Enabled = false
	decision, err = evaluator.Evaluate(quiet, testNotification(), QuietOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Active || decision.Action != "" {
		t.Fatalf("disabled quiet hours acted on event: %#v", decision)
	}
}

func TestQuietHoursDSTUsesLocalCivilEnd(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	quiet := settings.QuietHours{
		Enabled:  true,
		Timezone: "America/New_York",
		Action:   "defer",
		Intervals: []settings.QuietInterval{{
			Days: []string{"sun"}, Start: "00:30", End: "04:00",
		}},
	}
	tests := []struct {
		name string
		now  time.Time
		end  time.Time
	}{
		{
			name: "spring forward",
			now:  time.Date(2026, 3, 8, 1, 30, 0, 0, location),
			end:  time.Date(2026, 3, 8, 4, 0, 0, 0, location),
		},
		{
			name: "fall back",
			now:  time.Date(2026, 11, 1, 1, 30, 0, 0, location),
			end:  time.Date(2026, 11, 1, 4, 0, 0, 0, location),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evaluator := QuietEvaluator{Now: func() time.Time { return test.now }}
			decision, err := evaluator.Evaluate(quiet, testNotification(), QuietOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if !decision.Active || !decision.DeferUntil.Equal(test.end.UTC()) {
				t.Fatalf("DST decision = %#v, want end %s", decision, test.end.UTC())
			}
		})
	}
}
