package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errors.New("pipe closed")
}

func TestVersionJSON(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run([]string{"version", "--json"}, strings.NewReader(""), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	var value map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if value["version"] == "" || value["platform"] == "" {
		t.Fatalf("incomplete version output: %#v", value)
	}
}

func TestEmitAndDeduplicate(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("AGENTBELL_STATE_DIR", stateDir)
	raw := `{"hook_event_name":"Stop","session_id":"s1","turn_id":"t1"}`
	for index := 0; index < 2; index++ {
		var stderr bytes.Buffer
		code := Run(
			[]string{"emit", "--adapter", "codex", "--surface", "cli", "--runtime", "host", "--stdin"},
			strings.NewReader(raw),
			&bytes.Buffer{},
			&stderr,
		)
		if code != 0 {
			t.Fatalf("code=%d stderr=%s", code, stderr.String())
		}
	}
	entries, err := os.ReadDir(filepath.Join(stateDir, "queue", "pending"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one pending event, got %d", len(entries))
	}
}

func TestEmitInputLimitsAndFailOpen(t *testing.T) {
	var stderr bytes.Buffer
	code := Run(
		[]string{"emit", "--adapter", "codex", "--surface", "cli", "--runtime", "host", "--stdin"},
		strings.NewReader(strings.Repeat("x", maxInputSize+1)),
		&bytes.Buffer{},
		&stderr,
	)
	if code == 0 || !strings.Contains(stderr.String(), "exceeds") {
		t.Fatalf("expected size error: code=%d stderr=%s", code, stderr.String())
	}

	stderr.Reset()
	code = Run(
		[]string{"emit", "--adapter", "codex", "--surface", "cli", "--runtime", "host", "--stdin", "--fail-open"},
		strings.NewReader("{"),
		&bytes.Buffer{},
		&stderr,
	)
	if code != 0 {
		t.Fatalf("fail-open returned %d: %s", code, stderr.String())
	}
}

func TestReadLimitedBoundaries(t *testing.T) {
	if _, err := readLimited(strings.NewReader(" \n\t"), maxInputSize); err == nil {
		t.Fatal("empty input was accepted")
	}
	if _, err := readLimited(failingReader{}, maxInputSize); err == nil ||
		!strings.Contains(err.Error(), "pipe closed") {
		t.Fatalf("reader error was not returned: %v", err)
	}
	unicode := `{"message":"项目 🚀 𠮷"}`
	value, err := readLimited(strings.NewReader(unicode), maxInputSize)
	if err != nil || string(value) != unicode {
		t.Fatalf("unicode input: %q err=%v", value, err)
	}
	if _, err := readLimited(
		strings.NewReader(strings.Repeat("x", maxInputSize)),
		maxInputSize,
	); err != nil {
		t.Fatalf("exact-size input was rejected: %v", err)
	}
}

func TestHelpDoctorQueueAndUnsupported(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTBELL_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("AGENTBELL_CONFIG", filepath.Join(root, "missing-config.json"))
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	if code := Run(nil, strings.NewReader(""), &stdout, &stderr); code != 0 ||
		!strings.Contains(stdout.String(), "Usage:") {
		t.Fatalf("help failed: %d %s", code, stderr.String())
	}
	stdout.Reset()
	if code := Run([]string{"doctor", "--json"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
		t.Fatalf("doctor failed: %d %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"queue"`) {
		t.Fatalf("doctor output is incomplete: %s", stdout.String())
	}
	stdout.Reset()
	if code := Run(
		[]string{"queue", "list", "--state", "pending"},
		strings.NewReader(""),
		&stdout,
		&stderr,
	); code != 0 || strings.TrimSpace(stdout.String()) != "[]" {
		t.Fatalf("queue list failed: %d %s %s", code, stdout.String(), stderr.String())
	}
	stderr.Reset()
	if code := Run([]string{"unknown"}, strings.NewReader(""), &stdout, &stderr); code == 0 {
		t.Fatal("unsupported command succeeded")
	}
}

func TestAdapterCommands(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENTBELL_STATE_DIR", filepath.Join(root, "state"))
	t.Setenv("CODEX_HOME", filepath.Join(root, ".codex"))
	commands := [][]string{
		{"adapter", "plan", "codex"},
		{"adapter", "install", "codex", "--dry-run"},
		{"adapter", "install", "codex"},
		{"adapter", "verify", "codex"},
		{"adapter", "diagnose", "codex"},
		{"adapter", "uninstall", "codex"},
	}
	for _, arguments := range commands {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if code := Run(arguments, strings.NewReader(""), &stdout, &stderr); code != 0 {
			t.Fatalf("%v failed: %s", arguments, stderr.String())
		}
	}
}
