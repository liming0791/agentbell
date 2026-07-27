package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/liming0791/agentbell/core/internal/config"
	"github.com/liming0791/agentbell/core/internal/event"
	"github.com/liming0791/agentbell/core/internal/paths"
	"github.com/liming0791/agentbell/core/internal/settings"
)

func settingsCLIEnvironment(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	configPath := filepath.Join(root, "config.json")
	settingsPath := filepath.Join(root, "settings.json")
	value := config.Config{
		DefaultChannel: "primary",
		LarkCLIPath:    filepath.Join(root, "lark-cli"),
		Notifications: config.Notifications{
			Events:       []string{event.EventTaskCompleted},
			PrivacyLevel: event.PrivacyMetadataOnly,
		},
		Channels: []config.Channel{
			{
				ID: "primary", Name: "主群", Type: "feishu",
				ChatID: "oc_primary", As: "bot",
			},
			{
				ID: "spare", Name: "备用群", Type: "feishu",
				ChatID: "oc_spare", As: "user",
			},
		},
	}
	if err := config.Save(configPath, &value); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTBELL_CONFIG", configPath)
	t.Setenv("AGENTBELL_STATE_DIR", filepath.Join(root, "state"))
	return configPath, settingsPath
}

func runAppCommand(t *testing.T, arguments ...string) (int, string, string) {
	t.Helper()
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(
		arguments,
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	return code, stdout.String(), stderr.String()
}

func TestSettingsShowEffectiveAndEventMutation(t *testing.T) {
	_, settingsPath := settingsCLIEnvironment(t)
	code, stdout, stderr := runAppCommand(
		t,
		"settings", "show", "--effective", "--json",
	)
	if code != 0 {
		t.Fatalf("show effective failed: %s", stderr)
	}
	var shown struct {
		Source   string            `json:"source"`
		Settings settings.Settings `json:"settings"`
	}
	if err := json.Unmarshal([]byte(stdout), &shown); err != nil {
		t.Fatal(err)
	}
	if shown.Source != "legacy-config" ||
		!shown.Settings.Events[event.EventTaskCompleted] ||
		shown.Settings.Events[event.EventTaskFailed] ||
		shown.Settings.DefaultTemplate != "default" {
		t.Fatalf("unexpected effective settings: %#v", shown)
	}
	if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
		t.Fatalf("show created settings sidecar: %v", err)
	}

	code, _, stderr = runAppCommand(
		t,
		"settings", "event", "enable", event.EventTaskFailed,
	)
	if code != 0 {
		t.Fatalf("enable event failed: %s", stderr)
	}
	loaded, err := settings.Load(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Events[event.EventTaskFailed] {
		t.Fatal("event switch was not enabled")
	}

	code, _, stderr = runAppCommand(
		t,
		"settings", "event", "disable", event.EventTaskCompleted, "--dry-run",
	)
	if code != 0 {
		t.Fatalf("event dry-run failed: %s", stderr)
	}
	reloaded, err := settings.Load(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.Events[event.EventTaskCompleted] {
		t.Fatal("dry-run changed the event switch")
	}

	code, _, stderr = runAppCommand(
		t,
		"settings", "event", "enable", "unknown.event",
	)
	if code == 0 || !strings.Contains(stderr, "unknown event") {
		t.Fatalf("unknown event accepted: code=%d stderr=%s", code, stderr)
	}
}

func TestSettingsChannelRenameAndDefault(t *testing.T) {
	configPath, _ := settingsCLIEnvironment(t)
	code, _, stderr := runAppCommand(
		t,
		"settings", "channel", "rename", "spare",
		"--name", "值班群", "--dry-run",
	)
	if code != 0 {
		t.Fatalf("rename dry-run failed: %s", stderr)
	}
	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Channels[1].Name != "备用群" {
		t.Fatal("rename dry-run modified config")
	}

	code, _, stderr = runAppCommand(
		t,
		"settings", "channel", "rename", "spare", "--name", "值班群",
	)
	if code != 0 {
		t.Fatalf("rename failed: %s", stderr)
	}
	code, _, stderr = runAppCommand(
		t,
		"settings", "channel", "default", "spare",
	)
	if code != 0 {
		t.Fatalf("default failed: %s", stderr)
	}
	loaded, err = config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.DefaultChannel != "spare" || loaded.Channels[1].Name != "值班群" {
		t.Fatalf("channel changes were not persisted: %#v", loaded)
	}

	code, _, stderr = runAppCommand(
		t,
		"settings", "channel", "default", "missing",
	)
	if code == 0 || !strings.Contains(stderr, `channel "missing" not found`) {
		t.Fatalf("missing channel accepted: code=%d stderr=%s", code, stderr)
	}

	code, stdout, stderr := runAppCommand(
		t,
		"settings", "channel", "list", "--json",
	)
	if code != 0 {
		t.Fatalf("channel list failed: %s", stderr)
	}
	var snapshot config.ChannelSnapshot
	if err := json.Unmarshal([]byte(stdout), &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Config.Channels) != 2 || snapshot.Revision == "" {
		t.Fatalf("channel snapshot = %#v", snapshot)
	}

	code, _, stderr = runAppCommand(
		t,
		"settings", "channel", "add", "third",
		"--name", "第三群",
		"--chat-id", "oc_third",
		"--as", "bot",
		"--dry-run",
	)
	if code != 0 {
		t.Fatalf("channel add dry-run failed: %s", stderr)
	}
	loaded, err = config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Channels) != 2 {
		t.Fatal("channel add dry-run changed config")
	}

	code, _, stderr = runAppCommand(
		t,
		"settings", "channel", "add", "third",
		"--name", "第三群",
		"--chat-id", "oc_third",
		"--as", "bot",
	)
	if code != 0 {
		t.Fatalf("channel add failed: %s", stderr)
	}
	code, _, stderr = runAppCommand(
		t,
		"settings", "channel", "remove", "spare",
		"--replacement-default", "primary",
	)
	if code != 0 {
		t.Fatalf("channel remove failed: %s", stderr)
	}
	loaded, err = config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.DefaultChannel != "primary" ||
		len(loaded.Channels) != 2 ||
		loaded.Channels[1].ID != "third" {
		t.Fatalf("channel add/remove result = %#v", loaded)
	}
}

func TestSettingsTemplateLifecycleAndSafePreview(t *testing.T) {
	_, settingsPath := settingsCLIEnvironment(t)
	code, _, stderr := runAppCommand(
		t,
		"settings", "template", "set", "terse",
		"--body", "{{sourceName}} {{event}} {{project}}",
	)
	if code != 0 {
		t.Fatalf("template set failed: %s", stderr)
	}
	loaded, err := settings.Load(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Templates) != 2 {
		t.Fatalf("template was not added: %#v", loaded.Templates)
	}

	code, stdout, stderr := runAppCommand(
		t,
		"settings", "template", "list", "--json",
	)
	if code != 0 || !strings.Contains(stdout, `"id": "terse"`) {
		t.Fatalf("template list failed: code=%d stdout=%s stderr=%s", code, stdout, stderr)
	}

	code, stdout, stderr = runAppCommand(
		t,
		"settings", "template", "preview", "terse",
		"--event", event.EventTaskFailed,
		"--source", "codex",
		"--project", "agentbell",
	)
	if code != 0 || strings.TrimSpace(stdout) != "Codex task.failed agentbell" {
		t.Fatalf("template preview failed: code=%d stdout=%q stderr=%s", code, stdout, stderr)
	}

	code, _, stderr = runAppCommand(
		t,
		"settings", "template", "remove", "default",
	)
	if code == 0 || !strings.Contains(stderr, "default template") {
		t.Fatalf("default template removal was accepted: code=%d stderr=%s", code, stderr)
	}
	code, _, stderr = runAppCommand(
		t,
		"settings", "template", "remove", "terse",
	)
	if code != 0 {
		t.Fatalf("template remove failed: %s", stderr)
	}
}

func TestSettingsQuietHoursAndPolicyExplain(t *testing.T) {
	_, settingsPath := settingsCLIEnvironment(t)
	code, _, stderr := runAppCommand(
		t,
		"settings", "quiet-hours", "set",
		"--timezone", "Asia/Singapore",
		"--days", "mon,tue,wed,thu,fri",
		"--start", "22:00",
		"--end", "07:00",
		"--action", "defer",
	)
	if code != 0 {
		t.Fatalf("quiet-hours set failed: %s", stderr)
	}
	loaded, err := settings.Load(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.QuietHours.Enabled ||
		loaded.QuietHours.Timezone != "Asia/Singapore" ||
		len(loaded.QuietHours.Intervals) != 1 {
		t.Fatalf("quiet hours not persisted: %#v", loaded.QuietHours)
	}

	enabled := false
	loaded.Policies = []settings.Policy{{
		ID: "failures",
		Match: settings.PolicyMatch{
			Events:  []string{event.EventTaskFailed},
			Sources: []string{"codex"},
		},
		Action: settings.PolicyAction{Enabled: &enabled},
	}}
	if err := settings.Save(settingsPath, &loaded); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runAppCommand(t, "policy", "status", "--json")
	if code != 0 || !strings.Contains(stdout, `"policyCount": 1`) {
		t.Fatalf("policy status failed: code=%d stdout=%s stderr=%s", code, stdout, stderr)
	}
	code, stdout, stderr = runAppCommand(
		t,
		"policy", "explain",
		"--event", event.EventTaskFailed,
		"--source", "codex",
		"--at", "2026-07-27T12:00:00Z",
	)
	if code != 0 ||
		!strings.Contains(stdout, `"policyId": "failures"`) ||
		!strings.Contains(stdout, `"enabled": false`) {
		t.Fatalf("policy explain failed: code=%d stdout=%s stderr=%s", code, stdout, stderr)
	}

	code, _, stderr = runAppCommand(
		t,
		"settings", "quiet-hours", "disable",
	)
	if code != 0 {
		t.Fatalf("quiet-hours disable failed: %s", stderr)
	}
	loaded, err = settings.Load(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.QuietHours.Enabled {
		t.Fatal("quiet hours stayed enabled")
	}
}

func TestSettingsMutationsAreSerializedAndRecoverStaleLock(t *testing.T) {
	configPath, settingsPath := settingsCLIEnvironment(t)
	resolved := pathsForSettingsTest(configPath)
	lockPath := settingsPath + ".agentbell.lock"
	if err := os.Mkdir(lockPath, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-settingsLockStale - time.Second)
	if err := os.Chtimes(lockPath, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := updateSettings(resolved, false, func(value *settings.Settings) error {
		value.Events[event.EventTaskFailed] = true
		return nil
	}); err != nil {
		t.Fatalf("stale lock was not recovered: %v", err)
	}

	var wait sync.WaitGroup
	started := make(chan struct{})
	errs := make(chan error, 2)
	wait.Add(2)
	go func() {
		defer wait.Done()
		_, err := updateSettings(resolved, false, func(value *settings.Settings) error {
			close(started)
			time.Sleep(30 * time.Millisecond)
			value.Events[event.EventAgentWaiting] = true
			return nil
		})
		errs <- err
	}()
	<-started
	go func() {
		defer wait.Done()
		_, err := updateSettings(resolved, false, func(value *settings.Settings) error {
			value.Events[event.EventApprovalRequired] = true
			return nil
		})
		errs <- err
	}()
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := settings.Load(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Events[event.EventAgentWaiting] ||
		!loaded.Events[event.EventApprovalRequired] {
		t.Fatalf("concurrent setting was lost: %#v", loaded.Events)
	}
}

func pathsForSettingsTest(configPath string) paths.Paths {
	return paths.Paths{
		ConfigFile: configPath,
		StateDir:   filepath.Join(filepath.Dir(configPath), "state"),
		LogDir:     filepath.Join(filepath.Dir(configPath), "logs"),
	}
}
