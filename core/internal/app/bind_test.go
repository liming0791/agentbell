package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liming0791/agentbell/core/internal/binding"
	"github.com/liming0791/agentbell/core/internal/config"
)

type appBindingRunner struct {
	result      binding.SearchResult
	searchCalls int
	sendCalls   int
}

func (runner *appBindingRunner) SearchMessages(
	_ context.Context,
	_ binding.SearchRequest,
) (binding.SearchResult, error) {
	runner.searchCalls++
	return runner.result, nil
}

func (runner *appBindingRunner) SendVerification(
	_ context.Context,
	_ binding.VerificationRequest,
) error {
	runner.sendCalls++
	return nil
}

func TestBindCreateAndCompleteAtomicallyAddsChannel(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	configPath := filepath.Join(root, "config.json")
	t.Setenv("AGENTBELL_STATE_DIR", stateDir)
	t.Setenv("AGENTBELL_CONFIG", configPath)
	t.Setenv("AGENTBELL_LOG_DIR", filepath.Join(root, "logs"))
	t.Setenv("AGENTBELL_DATA_DIR", filepath.Join(root, "data"))
	if err := config.Save(configPath, &config.Config{
		DefaultChannel: "existing",
		LarkCLIPath:    filepath.Join(root, "lark-cli"),
		Notifications: config.Notifications{
			PrivacyLevel: "metadata-only",
		},
		Channels: []config.Channel{{
			ID: "existing", Name: "Existing", Type: "feishu",
			ChatID: "oc_existing", As: "bot",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	originalNow := bindingNow
	bindingNow = func() time.Time { return now }
	t.Cleanup(func() { bindingNow = originalNow })

	var createOutput bytes.Buffer
	var stderr bytes.Buffer
	if code := Run(
		[]string{
			"bind", "create",
			"--name", "Build alerts",
			"--as", "bot",
			"--ttl", "2m",
			"--json",
		},
		strings.NewReader(""),
		&createOutput,
		&stderr,
	); code != 0 {
		t.Fatalf("bind create failed: %s", stderr.String())
	}
	var created struct {
		Code      string    `json:"code"`
		ExpiresAt time.Time `json:"expiresAt"`
	}
	if err := json.Unmarshal(createOutput.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Code, "AGB-") ||
		!created.ExpiresAt.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("unexpected create result: %#v", created)
	}
	recordFiles, err := filepath.Glob(
		filepath.Join(stateDir, "bindings", "pending", "*.json"),
	)
	if err != nil || len(recordFiles) != 1 {
		t.Fatalf("pending records = %#v err=%v", recordFiles, err)
	}
	persisted, err := os.ReadFile(recordFiles[0])
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(persisted, []byte(created.Code)) {
		t.Fatal("binding store persisted the plaintext code")
	}

	runner := &appBindingRunner{result: binding.SearchResult{
		Messages: []binding.SearchMessage{{
			ChatID:      "oc_bound_destination",
			Identity:    "user",
			CreatedAt:   now.Add(time.Minute),
			BodyContent: json.RawMessage(`{"text":"` + created.Code + `"}`),
		}},
	}}
	originalRunner := newBindingRunner
	newBindingRunner = func(string) binding.Runner { return runner }
	t.Cleanup(func() { newBindingRunner = originalRunner })
	bindingNow = func() time.Time { return now.Add(90 * time.Second) }

	var completeOutput bytes.Buffer
	stderr.Reset()
	if code := Run(
		[]string{"bind", "complete", "--code-stdin", "--json"},
		strings.NewReader(created.Code+"\n"),
		&completeOutput,
		&stderr,
	); code != 0 {
		t.Fatalf("bind complete failed: %s", stderr.String())
	}
	if strings.Contains(completeOutput.String(), created.Code) ||
		strings.Contains(completeOutput.String(), "oc_bound_destination") {
		t.Fatalf("completion output leaked binding data: %s", completeOutput.String())
	}
	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Channels) != 2 ||
		loaded.Channels[1].Name != "Build alerts" ||
		loaded.Channels[1].ChatID != "oc_bound_destination" ||
		loaded.Channels[1].As != "bot" {
		t.Fatalf("bound config = %#v", loaded)
	}
	if runner.searchCalls != 1 || runner.sendCalls != 1 {
		t.Fatalf(
			"runner calls: search=%d send=%d",
			runner.searchCalls,
			runner.sendCalls,
		)
	}
	store := binding.NewStore(filepath.Join(stateDir, "bindings"))
	if _, err := store.Load(created.Code, now.Add(100*time.Second)); !errors.Is(
		err,
		binding.ErrConsumed,
	) {
		t.Fatalf("completed code remained usable: %v", err)
	}
}

func TestBindCancelAndStatusNeverRevealCodeHash(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTBELL_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("AGENTBELL_CONFIG", filepath.Join(root, "config.json"))
	t.Setenv("AGENTBELL_DATA_DIR", filepath.Join(root, "data"))
	now := time.Date(2026, 7, 27, 9, 0, 0, 0, time.UTC)
	originalNow := bindingNow
	bindingNow = func() time.Time { return now }
	t.Cleanup(func() { bindingNow = originalNow })
	store := binding.NewStore(filepath.Join(root, "state", "bindings"))
	code, record, err := store.Create("Cancel me", "user", 2*time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if exit := Run(
		[]string{"bind", "status", "--json"},
		strings.NewReader(""),
		&stdout,
		&stderr,
	); exit != 0 {
		t.Fatalf("status failed: %s", stderr.String())
	}
	if strings.Contains(stdout.String(), code) ||
		strings.Contains(stdout.String(), record.CodeHash) {
		t.Fatalf("status leaked binding secret: %s", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if exit := Run(
		[]string{"bind", "cancel", "--code-stdin", "--json"},
		strings.NewReader(code+"\n"),
		&stdout,
		&stderr,
	); exit != 0 {
		t.Fatalf("cancel failed: %s", stderr.String())
	}
	if strings.Contains(stdout.String(), code) ||
		strings.Contains(stdout.String(), record.CodeHash) {
		t.Fatalf("cancel leaked binding secret: %s", stdout.String())
	}
}

func TestBindCompleteInitializesFreshM1CompatibleConfig(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "state")
	configPath := filepath.Join(root, "config.json")
	larkCLIPath := filepath.Join(root, "bin", "lark-cli")
	t.Setenv("AGENTBELL_STATE_DIR", stateDir)
	t.Setenv("AGENTBELL_CONFIG", configPath)
	t.Setenv("AGENTBELL_DATA_DIR", filepath.Join(root, "data"))
	now := time.Date(2026, 7, 27, 11, 0, 0, 0, time.UTC)
	store := binding.NewStore(filepath.Join(stateDir, "bindings"))
	code, _, err := store.Create(
		"First channel",
		"user",
		2*time.Minute,
		now,
		larkCLIPath,
	)
	if err != nil {
		t.Fatal(err)
	}
	runner := &appBindingRunner{result: binding.SearchResult{
		Messages: []binding.SearchMessage{{
			ChatID:      "oc_first_bound",
			Identity:    "user",
			CreatedAt:   now.Add(time.Minute),
			BodyContent: json.RawMessage(`{"text":"` + code + `"}`),
		}},
	}}
	originalRunner := newBindingRunner
	originalNow := bindingNow
	newBindingRunner = func(command string) binding.Runner {
		if command != larkCLIPath {
			t.Fatalf("runner command = %q", command)
		}
		return runner
	}
	bindingNow = func() time.Time { return now.Add(90 * time.Second) }
	t.Cleanup(func() {
		newBindingRunner = originalRunner
		bindingNow = originalNow
	})

	var stderr bytes.Buffer
	if exit := Run(
		[]string{"bind", "complete", "--code-stdin", "--json"},
		strings.NewReader(code+"\n"),
		&bytes.Buffer{},
		&stderr,
	); exit != 0 {
		t.Fatalf("fresh bind complete failed: %s", stderr.String())
	}
	loaded, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.LarkCLIPath != larkCLIPath ||
		len(loaded.Channels) != 1 ||
		loaded.DefaultChannel != loaded.Channels[0].ID ||
		loaded.Channels[0].ChatID != "oc_first_bound" ||
		loaded.Notifications.PrivacyLevel != "metadata-only" {
		t.Fatalf("fresh bound config = %#v", loaded)
	}
}
