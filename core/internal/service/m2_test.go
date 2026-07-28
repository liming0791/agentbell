package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/liming0791/agentbell/core/internal/config"
	"github.com/liming0791/agentbell/core/internal/event"
	"github.com/liming0791/agentbell/core/internal/queue"
	"github.com/liming0791/agentbell/core/internal/settings"
)

type channelSender struct {
	calls  []string
	texts  map[string]string
	errors map[string]error
}

func (sender *channelSender) Send(
	_ context.Context,
	channel config.Channel,
	text string,
) error {
	sender.calls = append(sender.calls, channel.ID)
	if sender.texts == nil {
		sender.texts = map[string]string{}
	}
	sender.texts[channel.ID] = text
	return sender.errors[channel.ID]
}

type receiptChannelSender struct {
	channelSender
}

func (sender *receiptChannelSender) SendWithReceipt(
	_ context.Context,
	channel config.Channel,
	text string,
) (string, error) {
	sender.calls = append(sender.calls, channel.ID)
	if sender.texts == nil {
		sender.texts = map[string]string{}
	}
	sender.texts[channel.ID] = text
	return "message-" + channel.ID, sender.errors[channel.ID]
}

func m2Config() config.Config {
	return config.Config{
		DefaultChannel: "team",
		Notifications:  config.Notifications{Events: []string{event.EventTaskCompleted}},
		Channels: []config.Channel{
			{ID: "team", Type: "feishu", ChatID: "oc_team", As: "bot"},
			{ID: "alpha", Type: "feishu", ChatID: "oc_alpha", As: "bot"},
			{ID: "beta", Type: "feishu", ChatID: "oc_beta", As: "bot"},
			{ID: "ignored", Type: "feishu", ChatID: "oc_ignored", As: "bot"},
		},
	}
}

func m2Settings() settings.Settings {
	events := settings.EventSwitches{}
	for _, name := range event.KnownEvents() {
		events[name] = name == event.EventTaskCompleted
	}
	return settings.Settings{
		Version:         settings.Version,
		MinCoreVersion:  "2.0.0",
		Events:          events,
		DefaultTemplate: "standard",
		Templates: []settings.Template{
			{ID: "standard", Body: "standard {{event}}"},
			{ID: "terse", Body: "{{sourceName}}/{{project}}/{{status}}"},
		},
		QuietHours: settings.QuietHours{},
		Policies: []settings.Policy{
			{
				ID:    "first",
				Match: settings.PolicyMatch{Projects: []string{"agentbell"}},
				Action: settings.PolicyAction{
					ChannelIDs: []string{"alpha", "beta"},
					TemplateID: "terse",
				},
			},
			{
				ID:    "second",
				Match: settings.PolicyMatch{Projects: []string{"agentbell"}},
				Action: settings.PolicyAction{
					ChannelIDs: []string{"ignored"},
					TemplateID: "standard",
				},
			},
		},
	}
}

func m2Service(
	queueValue *queue.Queue,
	sender Sender,
	now *time.Time,
	loadSettings func() (settings.Settings, error),
) Service {
	return Service{
		Queue: queueValue,
		LoadConfig: func() (config.Config, error) {
			return m2Config(), nil
		},
		Sender: sender,
		Now:    func() time.Time { return *now },
		Backoff: []time.Duration{
			time.Minute,
			time.Minute,
		},
		Processor: &Processor{LoadSettings: loadSettings},
	}
}

func TestM2MissingSidecarKeepsM1Behavior(t *testing.T) {
	queueValue, err := queue.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	enqueue(t, queueValue, "m2-missing-sidecar", now)
	sender := &channelSender{errors: map[string]error{}}
	serviceValue := m2Service(
		queueValue,
		sender,
		&now,
		func() (settings.Settings, error) {
			return settings.Settings{}, settings.ErrNotFound
		},
	)

	processed, err := serviceValue.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("process: processed=%v err=%v", processed, err)
	}
	if len(sender.calls) != 1 || sender.calls[0] != "team" {
		t.Fatalf("did not use M1 default channel: %#v", sender.calls)
	}
	history, err := queueValue.List(queue.StateSucceeded)
	if err != nil || len(history) != 1 || history[0].Ledger != nil {
		t.Fatalf("M1 fallback persisted M2 state: %#v err=%v", history, err)
	}
}

func TestM2PolicyFirstMatchFansOutAndRendersTemplate(t *testing.T) {
	queueValue, err := queue.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	enqueue(t, queueValue, "m2-fanout", now)
	sender := &channelSender{errors: map[string]error{}}
	serviceValue := m2Service(
		queueValue,
		sender,
		&now,
		func() (settings.Settings, error) { return m2Settings(), nil },
	)

	processed, err := serviceValue.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("process: processed=%v err=%v", processed, err)
	}
	if len(sender.calls) != 2 ||
		sender.calls[0] != "alpha" ||
		sender.calls[1] != "beta" {
		t.Fatalf("policy did not use ordered first match: %#v", sender.calls)
	}
	if sender.texts["alpha"] != "codex/agentbell/completed" ||
		sender.texts["beta"] != sender.texts["alpha"] {
		t.Fatalf("unexpected M2 rendering: %#v", sender.texts)
	}
	history, err := queueValue.List(queue.StateSucceeded)
	if err != nil || len(history) != 1 {
		t.Fatalf("history: %#v err=%v", history, err)
	}
	if history[0].QueueVersion != 1 ||
		history[0].Disposition != queue.DispositionSucceeded ||
		len(history[0].Ledger) != 2 {
		t.Fatalf("missing fan-out ledger: %#v", history[0])
	}
}

func TestM2ReceiptSenderPersistsMessageIDs(t *testing.T) {
	queueValue, err := queue.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	enqueue(t, queueValue, "m2-receipts", now)
	sender := &receiptChannelSender{
		channelSender: channelSender{errors: map[string]error{}},
	}
	serviceValue := m2Service(
		queueValue,
		sender,
		&now,
		func() (settings.Settings, error) { return m2Settings(), nil },
	)
	serviceValue.Processor.SourceName = func(event.Notification) string {
		return "Codex Desktop"
	}

	if processed, err := serviceValue.ProcessOne(context.Background()); !processed || err != nil {
		t.Fatalf("process: processed=%v err=%v", processed, err)
	}
	history, err := queueValue.List(queue.StateSucceeded)
	if err != nil || len(history) != 1 {
		t.Fatalf("history: %#v err=%v", history, err)
	}
	if history[0].Ledger[0].MessageID != "message-alpha" ||
		history[0].Ledger[1].MessageID != "message-beta" {
		t.Fatalf("message ids were not persisted: %#v", history[0].Ledger)
	}
	if sender.texts["alpha"] != "Codex Desktop/agentbell/completed" {
		t.Fatalf("custom source name was not rendered: %#v", sender.texts)
	}
}

func TestM2DisabledAndQuietDropAcknowledgeWithoutSending(t *testing.T) {
	tests := map[string]func(*settings.Settings){
		"disabled": func(value *settings.Settings) {
			value.Events[event.EventTaskCompleted] = false
		},
		"quiet-drop": func(value *settings.Settings) {
			value.QuietHours = settings.QuietHours{
				Enabled:  true,
				Timezone: "UTC",
				Action:   "drop",
				Intervals: []settings.QuietInterval{{
					Days: []string{"mon"}, Start: "22:00", End: "23:00",
				}},
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			queueValue, err := queue.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 7, 27, 22, 30, 0, 0, time.UTC)
			enqueue(t, queueValue, "m2-"+name, now)
			sender := &channelSender{errors: map[string]error{}}
			settingsValue := m2Settings()
			mutate(&settingsValue)
			serviceValue := m2Service(
				queueValue,
				sender,
				&now,
				func() (settings.Settings, error) { return settingsValue, nil },
			)
			if processed, err := serviceValue.ProcessOne(context.Background()); !processed || err != nil {
				t.Fatalf("process: processed=%v err=%v", processed, err)
			}
			if len(sender.calls) != 0 {
				t.Fatalf("suppressed event was sent: %#v", sender.calls)
			}
			history, err := queueValue.List(queue.StateSucceeded)
			if err != nil || len(history) != 1 || history[0].Ledger != nil {
				t.Fatalf("suppressed history: %#v err=%v", history, err)
			}
		})
	}
}

func TestM2InvalidRouteRetriesBeforeLedgerSnapshot(t *testing.T) {
	queueValue, err := queue.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	enqueue(t, queueValue, "m2-invalid-route", now)
	sender := &channelSender{errors: map[string]error{}}
	settingsValue := m2Settings()
	settingsValue.Policies[0].Action.ChannelIDs = []string{"missing"}
	serviceValue := m2Service(
		queueValue,
		sender,
		&now,
		func() (settings.Settings, error) { return settingsValue, nil },
	)

	if processed, err := serviceValue.ProcessOne(context.Background()); !processed || err == nil {
		t.Fatalf("expected route error: processed=%v err=%v", processed, err)
	}
	pending, err := queueValue.List(queue.StatePending)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending: %#v err=%v", pending, err)
	}
	if pending[0].Ledger != nil || pending[0].Attempts != 1 || len(sender.calls) != 0 {
		t.Fatalf("invalid route committed M2 delivery state: %#v calls=%#v", pending[0], sender.calls)
	}
}

func TestM2QuietDropFinishesExistingLedgerWithoutSending(t *testing.T) {
	queueValue, err := queue.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 7, 27, 22, 0, 0, 0, time.UTC)
	enqueue(t, queueValue, "m2-drop-existing", start)
	item, err := queueValue.Claim(start, time.Minute)
	if err != nil || item == nil {
		t.Fatalf("claim: item=%#v err=%v", item, err)
	}
	if _, err := queueValue.ResolveTargets(item, []queue.DeliveryTarget{
		{ChannelID: "alpha", TemplateID: "terse"},
		{ChannelID: "beta", TemplateID: "terse"},
	}, start); err != nil {
		t.Fatal(err)
	}
	now := start.Add(30 * time.Minute)
	if err := queueValue.Defer(item, start, now); err != nil {
		t.Fatal(err)
	}

	settingsValue := m2Settings()
	settingsValue.QuietHours = settings.QuietHours{
		Enabled:  true,
		Timezone: "UTC",
		Action:   "drop",
		Intervals: []settings.QuietInterval{{
			Days: []string{"mon"}, Start: "22:00", End: "23:00",
		}},
	}
	sender := &channelSender{errors: map[string]error{}}
	serviceValue := m2Service(
		queueValue,
		sender,
		&now,
		func() (settings.Settings, error) { return settingsValue, nil },
	)
	if processed, err := serviceValue.ProcessOne(context.Background()); !processed || err != nil {
		t.Fatalf("process: processed=%v err=%v", processed, err)
	}
	if len(sender.calls) != 0 {
		t.Fatalf("drop sent existing ledger: %#v", sender.calls)
	}
	history, err := queueValue.List(queue.StateSucceeded)
	if err != nil || len(history) != 1 ||
		history[0].Disposition != queue.DispositionDead {
		t.Fatalf("dropped ledger was not terminal: %#v err=%v", history, err)
	}
}

func TestM2ProcessorWithoutSettingsLoaderRetries(t *testing.T) {
	queueValue, err := queue.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	enqueue(t, queueValue, "m2-no-loader", now)
	serviceValue := Service{
		Queue: queueValue,
		LoadConfig: func() (config.Config, error) {
			return m2Config(), nil
		},
		Sender:    &channelSender{errors: map[string]error{}},
		Now:       func() time.Time { return now },
		Backoff:   []time.Duration{time.Minute, time.Minute},
		Processor: &Processor{},
	}
	if processed, err := serviceValue.ProcessOne(context.Background()); !processed || err == nil {
		t.Fatalf("expected loader error: processed=%v err=%v", processed, err)
	}
	pending, err := queueValue.List(queue.StatePending)
	if err != nil || len(pending) != 1 || pending[0].Attempts != 1 {
		t.Fatalf("missing loader was not retried: %#v err=%v", pending, err)
	}
}

func TestM2QuietHoursDefersWithoutAttempts(t *testing.T) {
	queueValue, err := queue.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 27, 22, 30, 0, 0, time.UTC)
	enqueue(t, queueValue, "m2-quiet", now)
	sender := &channelSender{errors: map[string]error{}}
	settingsValue := m2Settings()
	settingsValue.QuietHours = settings.QuietHours{
		Enabled:  true,
		Timezone: "UTC",
		Action:   "defer",
		Intervals: []settings.QuietInterval{{
			Days: []string{"mon"}, Start: "22:00", End: "23:00",
		}},
	}
	serviceValue := m2Service(
		queueValue,
		sender,
		&now,
		func() (settings.Settings, error) { return settingsValue, nil },
	)

	processed, err := serviceValue.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("process: processed=%v err=%v", processed, err)
	}
	if len(sender.calls) != 0 {
		t.Fatalf("quiet event was sent: %#v", sender.calls)
	}
	pending, err := queueValue.List(queue.StatePending)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending: %#v err=%v", pending, err)
	}
	if pending[0].Attempts != 0 ||
		pending[0].Ledger[0].Attempts != 0 ||
		!pending[0].NextAttemptAt.Equal(time.Date(2026, 7, 27, 23, 0, 0, 0, time.UTC)) {
		t.Fatalf("quiet defer changed retry accounting: %#v", pending[0])
	}
}

func TestM2PartialSuccessRetryDoesNotResendSucceededChannel(t *testing.T) {
	queueValue, err := queue.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	enqueue(t, queueValue, "m2-partial", now)
	sender := &channelSender{
		errors: map[string]error{"beta": errors.New("offline")},
	}
	serviceValue := m2Service(
		queueValue,
		sender,
		&now,
		func() (settings.Settings, error) { return m2Settings(), nil },
	)

	if processed, err := serviceValue.ProcessOne(context.Background()); !processed || err == nil {
		t.Fatalf("expected partial error: processed=%v err=%v", processed, err)
	}
	pending, err := queueValue.List(queue.StatePending)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending: %#v err=%v", pending, err)
	}
	if pending[0].Disposition != queue.DispositionPartial ||
		pending[0].Ledger[0].State != queue.DeliverySucceeded ||
		pending[0].Ledger[1].State != queue.DeliveryPending {
		t.Fatalf("unexpected partial ledger: %#v", pending[0])
	}

	delete(sender.errors, "beta")
	now = now.Add(time.Minute)
	if processed, err := serviceValue.ProcessOne(context.Background()); !processed || err != nil {
		t.Fatalf("retry: processed=%v err=%v", processed, err)
	}
	if len(sender.calls) != 3 ||
		sender.calls[0] != "alpha" ||
		sender.calls[1] != "beta" ||
		sender.calls[2] != "beta" {
		t.Fatalf("successful channel was resent: %#v", sender.calls)
	}
	stats, err := queueValue.Stats()
	if err != nil || stats.History != 1 || stats.Pending != 0 {
		t.Fatalf("partial retry did not finish: %#v err=%v", stats, err)
	}
}

func TestM2PermanentTargetFailureFinishesEnvelope(t *testing.T) {
	queueValue, err := queue.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	enqueue(t, queueValue, "m2-target-dead", now)
	sender := &channelSender{
		errors: map[string]error{"alpha": permanentFailure{}},
	}
	serviceValue := m2Service(
		queueValue,
		sender,
		&now,
		func() (settings.Settings, error) { return m2Settings(), nil },
	)

	if processed, err := serviceValue.ProcessOne(context.Background()); !processed || err == nil {
		t.Fatalf("expected target failure: processed=%v err=%v", processed, err)
	}
	history, err := queueValue.List(queue.StateSucceeded)
	if err != nil || len(history) != 1 {
		t.Fatalf("history: %#v err=%v", history, err)
	}
	if history[0].Disposition != queue.DispositionPartial ||
		history[0].Ledger[0].State != queue.DeliveryDead ||
		history[0].Ledger[1].State != queue.DeliverySucceeded {
		t.Fatalf("unexpected terminal ledger: %#v", history[0])
	}
}

func TestM2LedgerDoesNotFallBackWhenSidecarDisappears(t *testing.T) {
	queueValue, err := queue.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	enqueue(t, queueValue, "m2-sidecar-disappears", now)
	sender := &channelSender{
		errors: map[string]error{"beta": errors.New("offline")},
	}
	loadCount := 0
	serviceValue := m2Service(
		queueValue,
		sender,
		&now,
		func() (settings.Settings, error) {
			loadCount++
			if loadCount > 1 {
				return settings.Settings{}, settings.ErrNotFound
			}
			return m2Settings(), nil
		},
	)
	if _, err := serviceValue.ProcessOne(context.Background()); err == nil {
		t.Fatal("expected initial partial error")
	}

	now = now.Add(time.Minute)
	if processed, err := serviceValue.ProcessOne(context.Background()); !processed || err == nil {
		t.Fatalf("expected missing settings retry: processed=%v err=%v", processed, err)
	}
	if len(sender.calls) != 2 {
		t.Fatalf("missing settings caused a legacy resend: %#v", sender.calls)
	}
	history, err := queueValue.List(queue.StateSucceeded)
	if err != nil || len(history) != 1 ||
		history[0].Ledger[0].State != queue.DeliverySucceeded ||
		history[0].Ledger[1].State != queue.DeliveryDead {
		t.Fatalf("partial ledger was not preserved: %#v err=%v", history, err)
	}
}
