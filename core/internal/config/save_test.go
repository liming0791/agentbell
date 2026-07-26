package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func validConfig(t *testing.T) *Config {
	t.Helper()
	return &Config{
		DefaultChannel: "feishu",
		LarkCLIPath:    filepath.Join(t.TempDir(), "bin", "lark-cli"),
		Notifications: Notifications{
			Events:       []string{"task.completed", "task.failed", "approval.required"},
			PrivacyLevel: "metadata-only",
		},
		Channels: []Channel{
			{ID: "feishu", Name: "AgentBell 通知", Type: "feishu", ChatID: "oc_test", As: "bot"},
		},
	}
}

func TestSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	value := validConfig(t)
	if err := Save(path, value); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.DefaultChannel != "feishu" || loaded.Channels[0].ChatID != "oc_test" ||
		loaded.Notifications.PrivacyLevel != "metadata-only" ||
		loaded.LarkCLIPath != value.LarkCLIPath {
		t.Fatalf("round-trip mismatch: %#v", loaded)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("expected mode 0600, got %o", info.Mode().Perm())
	}
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && parent.Mode().Perm() != 0o700 {
		t.Fatalf("expected parent mode 0700, got %o", parent.Mode().Perm())
	}
}

func TestSaveInvalidWritesNothing(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config.json")
	invalid := validConfig(t)
	invalid.DefaultChannel = "missing"
	if err := Save(path, invalid); err == nil {
		t.Fatal("expected validation error")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("invalid config must not be written: %v", err)
	}
	if err := Save(path, nil); err == nil {
		t.Fatal("expected nil config error")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tmp") {
			t.Fatalf("temporary file left behind: %s", entry.Name())
		}
	}
}

func TestSaveOverwriteIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := Save(path, validConfig(t)); err != nil {
		t.Fatal(err)
	}
	updated := validConfig(t)
	updated.Channels[0].ChatID = "oc_updated"
	for index := 0; index < 2; index++ {
		if err := Save(path, updated); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Channels[0].ChatID != "oc_updated" {
		t.Fatalf("overwrite failed: %#v", loaded.Channels[0])
	}
}
