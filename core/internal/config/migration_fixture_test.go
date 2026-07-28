package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/liming0791/agentbell/core/internal/event"
)

func TestMigrationFixtureConfigV1(t *testing.T) {
	fixture := readConfigMigrationFixture(t, "config-v1.json")
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, fixture, 0o600); err != nil {
		t.Fatal(err)
	}

	migrated, err := Load(path)
	if err != nil {
		t.Fatalf("load Config v1 fixture: %v", err)
	}
	if migrated.Notifications.PrivacyLevel != event.PrivacyMetadataOnly {
		t.Fatalf(
			"Config v1 privacy migrated to %q, want %q",
			migrated.Notifications.PrivacyLevel,
			event.PrivacyMetadataOnly,
		)
	}
	if migrated.Notifications.IncludeSummary {
		t.Fatal("Config v1 migration enabled summary collection")
	}
	if migrated.DefaultChannel != "fixture" ||
		len(migrated.Channels) != 1 ||
		migrated.Channels[0].ChatID != "fixture_chat_id_not_real" {
		t.Fatalf("Config v1 fields changed during migration: %#v", migrated)
	}

	if err := Save(path, &migrated); err != nil {
		t.Fatalf("persist migrated Config v1: %v", err)
	}
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(persisted, []byte(`"privacyLevel": "metadata-only"`)) {
		t.Fatalf("migrated config did not persist its privacy default: %s", persisted)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(migrated, reloaded) {
		t.Fatalf("Config v1 migration was not stable: %#v != %#v", migrated, reloaded)
	}

	var document map[string]any
	if err := json.Unmarshal(persisted, &document); err != nil {
		t.Fatal(err)
	}
	notifications, ok := document["notifications"].(map[string]any)
	if !ok ||
		notifications["privacyLevel"] != event.PrivacyMetadataOnly ||
		notifications["includeSummary"] != false {
		t.Fatalf("unexpected migrated privacy document: %#v", notifications)
	}
}

func TestMigrationFixtureConfigV1PreservesSummaryOptIn(t *testing.T) {
	fixture := bytes.Replace(
		readConfigMigrationFixture(t, "config-v1.json"),
		[]byte(`"includeSummary": false`),
		[]byte(`"includeSummary": true`),
		1,
	)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	migrated, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !migrated.Notifications.IncludeSummary ||
		migrated.Notifications.PrivacyLevel != event.PrivacySummary {
		t.Fatalf("Config v1 summary opt-in changed: %#v", migrated.Notifications)
	}
}

func readConfigMigrationFixture(t *testing.T, name string) []byte {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve migration fixture source")
	}
	value, err := os.ReadFile(filepath.Join(
		filepath.Dir(source),
		"..",
		"..",
		"testdata",
		"migrations",
		name,
	))
	if err != nil {
		t.Fatal(err)
	}
	return value
}
