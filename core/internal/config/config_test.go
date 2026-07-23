package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAndValidate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	value := `{
		"defaultChannel":"team",
		"notifications":{"events":["task.completed"],"includeSummary":false},
		"channels":[{"id":"team","name":"Team","type":"feishu","chatId":"oc_test","as":"bot"}]
	}`
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !config.EventEnabled("task.completed") || config.EventEnabled("task.failed") {
		t.Fatal("event filter is incorrect")
	}
	if channel, ok := config.Default(); !ok || channel.ChatID != "oc_test" {
		t.Fatalf("default channel not resolved: %#v %v", channel, ok)
	}
}

func TestLoadErrors(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.json")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	path := filepath.Join(t.TempDir(), "invalid.json")
	if err := os.WriteFile(path, []byte(`{"unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected strict config error")
	}
}
