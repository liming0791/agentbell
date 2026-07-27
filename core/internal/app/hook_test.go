package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/liming0791/agentbell/core/internal/hookaudit"
)

func TestHookConflictsReportsStableBridgePlan(t *testing.T) {
	_, _, _, _ = activeRuntimeFixture(t)
	hookPath := filepath.Join(os.Getenv("CODEX_HOME"), "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hookPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(
		[]string{"hook", "conflicts", "codex", "--json"},
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("hook conflicts failed: %s", stderr.String())
	}
	var report hookaudit.Report
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Summary.MissingStableBridge != 1 ||
		report.Plan.Blocked ||
		len(report.Plan.Actions) != 1 ||
		report.Plan.Actions[0].Operation != hookaudit.OperationAdd {
		t.Fatalf("unexpected hook conflict report: %#v", report)
	}
}

func TestHookReconcileInstallsOnlyManagedStableHook(t *testing.T) {
	_, _, _, _ = activeRuntimeFixture(t)
	hookPath := filepath.Join(os.Getenv("CODEX_HOME"), "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o700); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{
	  "hooks": {
	    "Stop": [{
	      "hooks": [{
	        "type": "command",
	        "command": "\"/external/notifier\" --done",
	        "commandWindows": "\"C:\\external\\notifier.exe\" --done"
	      }]
	    }]
	  }
	}
`)
	if err := os.WriteFile(hookPath, original, 0o600); err != nil {
		t.Fatal(err)
	}

	var dryRun bytes.Buffer
	var stderr bytes.Buffer
	if code := Run(
		[]string{"hook", "reconcile", "codex", "--dry-run", "--json"},
		strings.NewReader(""),
		&dryRun,
		&stderr,
	); code != 0 {
		t.Fatalf("hook reconcile dry-run failed: %s", stderr.String())
	}
	afterDryRun, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterDryRun, original) {
		t.Fatal("hook reconcile dry-run changed the source file")
	}

	var stdout bytes.Buffer
	stderr.Reset()
	if code := Run(
		[]string{"hook", "reconcile", "codex", "--json"},
		strings.NewReader(""),
		&stdout,
		&stderr,
	); code != 0 {
		t.Fatalf("hook reconcile failed: %s", stderr.String())
	}
	var result struct {
		After hookaudit.Report `json:"after"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.After.Plan.Blocked ||
		result.After.Summary.CurrentStableBridge != 1 ||
		result.After.Summary.ExternalSameEvent != 1 ||
		len(result.After.Plan.Actions) != 0 {
		t.Fatalf("unexpected reconciled report: %#v", result.After)
	}
	raw, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte("/external/notifier")) {
		t.Fatal("hook reconcile deleted an external Hook")
	}
}
