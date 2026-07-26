package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liming0791/agentbell/core/internal/config"
	"github.com/liming0791/agentbell/core/internal/event"
	"github.com/liming0791/agentbell/core/internal/queue"
	"github.com/liming0791/agentbell/core/internal/transport"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "im" {
		output := os.Getenv("AGENTBELL_FAKE_LARK_OUTPUT")
		if output == "" {
			os.Exit(19)
		}
		if err := os.WriteFile(output, []byte(strings.Join(os.Args[1:], "\n")), 0o600); err != nil {
			os.Exit(20)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type fakeSender struct {
	count int
	err   error
	text  string
}

type permanentFailure struct{}

func (permanentFailure) Error() string {
	return "permanent failure"
}

func (permanentFailure) Permanent() bool {
	return true
}

func (sender *fakeSender) Send(_ context.Context, _ config.Channel, text string) error {
	sender.count++
	sender.text = text
	return sender.err
}

func validConfig() config.Config {
	return config.Config{
		DefaultChannel: "team",
		Notifications:  config.Notifications{Events: []string{event.EventTaskCompleted}},
		Channels: []config.Channel{{
			ID: "team", Type: "feishu", ChatID: "oc_test", As: "bot",
		}},
	}
}

func enqueue(t *testing.T, queueValue *queue.Queue, key string, now time.Time) {
	t.Helper()
	_, _, err := queueValue.Enqueue(event.Notification{
		Version:        event.Version,
		Source:         "codex",
		Surface:        "cli",
		Runtime:        "host",
		Event:          event.EventTaskCompleted,
		Status:         event.StatusCompleted,
		OccurredAt:     now,
		IdempotencyKey: key,
		Priority:       event.PriorityNormal,
		PrivacyLevel:   event.PrivacyMetadataOnly,
		Project:        "agentbell",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
}

func TestProcessOneDeliversAndAcknowledges(t *testing.T) {
	queueValue, err := queue.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	enqueue(t, queueValue, "service-success", now)
	sender := &fakeSender{}
	service := Service{
		Queue: queueValue,
		LoadConfig: func() (config.Config, error) {
			return validConfig(), nil
		},
		Sender: sender,
		Now:    func() time.Time { return now },
	}
	processed, err := service.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("process: %v %v", processed, err)
	}
	if sender.count != 1 || sender.text == "" {
		t.Fatalf("sender was not called: %#v", sender)
	}
	stats, _ := queueValue.Stats()
	if stats.History != 1 {
		t.Fatalf("event not acknowledged: %#v", stats)
	}
}

func TestProcessOneBuildsSenderFromLoadedConfig(t *testing.T) {
	queueValue, err := queue.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	enqueue(t, queueValue, "sender-factory", now)
	settings := validConfig()
	settings.LarkCLIPath = "/absolute/node/bin/lark-cli"
	sender := &fakeSender{}
	var receivedPath string
	serviceValue := Service{
		Queue: queueValue,
		LoadConfig: func() (config.Config, error) {
			return settings, nil
		},
		SenderFactory: func(loaded config.Config) Sender {
			receivedPath = loaded.LarkCLIPath
			return sender
		},
		Now: func() time.Time { return now },
	}
	processed, err := serviceValue.ProcessOne(context.Background())
	if err != nil || !processed {
		t.Fatalf("process: %v %v", processed, err)
	}
	if receivedPath != settings.LarkCLIPath || sender.count != 1 {
		t.Fatalf("factory did not receive config: path=%q sender=%#v", receivedPath, sender)
	}
}

func TestProcessOneRetriesFailures(t *testing.T) {
	queueValue, err := queue.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	enqueue(t, queueValue, "service-failure", now)
	service := Service{
		Queue: queueValue,
		LoadConfig: func() (config.Config, error) {
			return validConfig(), nil
		},
		Sender:  &fakeSender{err: errors.New("offline")},
		Now:     func() time.Time { return now },
		Backoff: []time.Duration{time.Second, time.Second},
	}
	processed, err := service.ProcessOne(context.Background())
	if !processed || err == nil {
		t.Fatalf("expected retry error: %v %v", processed, err)
	}
	stats, _ := queueValue.Stats()
	if stats.Pending != 1 {
		t.Fatalf("event not retried: %#v", stats)
	}
}

func TestProcessOneDeadLettersPermanentFailure(t *testing.T) {
	queueValue, err := queue.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	enqueue(t, queueValue, "service-permanent", now)
	serviceValue := Service{
		Queue: queueValue,
		LoadConfig: func() (config.Config, error) {
			return validConfig(), nil
		},
		Sender: &fakeSender{err: permanentFailure{}},
		Now:    func() time.Time { return now },
	}
	if processed, err := serviceValue.ProcessOne(context.Background()); !processed || err == nil {
		t.Fatalf("expected permanent error: processed=%v err=%v", processed, err)
	}
	stats, err := queueValue.Stats()
	if err != nil || stats.Dead != 1 || stats.Pending != 0 {
		t.Fatalf("permanent event was not dead-lettered: %#v err=%v", stats, err)
	}
}

func TestServiceLockRejectsSecondInstance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.lock")
	first, err := acquireLock(path, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	if _, err := acquireLock(path, time.Minute); err == nil {
		t.Fatal("expected second lock to fail")
	}
}

func TestProcessOneConfigAndDisabledEvents(t *testing.T) {
	queueValue, err := queue.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	enqueue(t, queueValue, "config-failure", now)
	serviceValue := Service{
		Queue: queueValue,
		LoadConfig: func() (config.Config, error) {
			return config.Config{}, errors.New("missing config")
		},
		Sender:  &fakeSender{},
		Now:     func() time.Time { return now },
		Backoff: []time.Duration{time.Second, time.Second},
	}
	if processed, err := serviceValue.ProcessOne(context.Background()); !processed || err == nil {
		t.Fatalf("expected config retry: %v %v", processed, err)
	}

	queueValue, err = queue.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	enqueue(t, queueValue, "disabled", now)
	settings := validConfig()
	settings.Notifications.Events = []string{event.EventTaskFailed}
	sender := &fakeSender{}
	serviceValue = Service{
		Queue: queueValue,
		LoadConfig: func() (config.Config, error) {
			return settings, nil
		},
		Sender: sender,
		Now:    func() time.Time { return now },
	}
	if processed, err := serviceValue.ProcessOne(context.Background()); !processed || err != nil {
		t.Fatalf("disabled event failed: %v %v", processed, err)
	}
	if sender.count != 0 {
		t.Fatal("disabled event was sent")
	}
}

func TestRunStopsOnCancelledContext(t *testing.T) {
	queueValue, err := queue.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	serviceValue := Service{
		Queue: queueValue,
		LoadConfig: func() (config.Config, error) {
			return validConfig(), nil
		},
		Sender:       &fakeSender{},
		PollInterval: time.Millisecond,
	}
	if err := serviceValue.Run(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestIncompleteServiceDependencies(t *testing.T) {
	if _, err := (&Service{}).ProcessOne(context.Background()); err == nil {
		t.Fatal("expected dependency error")
	}
}

func TestRawHookToHistoryWithFakeLarkExecutable(t *testing.T) {
	root := t.TempDir()
	queueValue, err := queue.Open(filepath.Join(root, "queue"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	notification, err := event.Normalize(
		"codex",
		"cli",
		"host",
		[]byte(`{"hook_event_name":"Stop","session_id":"private","turn_id":"turn-1"}`),
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := queueValue.Enqueue(notification, now); err != nil {
		t.Fatal(err)
	}

	output := filepath.Join(root, "lark-arguments.txt")
	t.Setenv("AGENTBELL_FAKE_LARK_OUTPUT", output)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	serviceValue := Service{
		Queue: queueValue,
		LoadConfig: func() (config.Config, error) {
			return validConfig(), nil
		},
		Sender: transport.LarkCLI{
			Command: executable,
			Timeout: 10 * time.Second,
		},
		Now: func() time.Time { return now },
	}
	if processed, err := serviceValue.ProcessOne(context.Background()); !processed || err != nil {
		t.Fatalf("end-to-end process: processed=%v err=%v", processed, err)
	}
	arguments, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(arguments), "oc_test") ||
		!strings.Contains(string(arguments), "AgentBell") {
		t.Fatalf("unexpected lark arguments: %s", arguments)
	}
	history, err := queueValue.List(queue.StateSucceeded)
	if err != nil || len(history) != 1 {
		t.Fatalf("history: %#v err=%v", history, err)
	}
	if history[0].Event.SessionID == "private" ||
		history[0].Event.Summary != "" ||
		history[0].Event.CWD != "" {
		t.Fatalf("private hook data was persisted: %#v", history[0])
	}
}
