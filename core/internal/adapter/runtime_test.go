package adapter

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRuntimeProofAfterConfig(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "hooks.json")
	if err := os.WriteFile(configPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	configTime := time.Date(2026, 7, 25, 8, 0, 0, 0, time.UTC)
	if err := os.Chtimes(configPath, configTime, configTime); err != nil {
		t.Fatal(err)
	}

	staleTime := configTime.Add(-time.Second)
	if err := RecordRuntimeProof(root, codexAdapterID, "approval.required", staleTime); err != nil {
		t.Fatal(err)
	}
	proof, verified := runtimeProofAfterConfig(root, codexAdapterID, configPath)
	if verified || !proof.LastSeen.Equal(staleTime) {
		t.Fatalf("stale proof was accepted: %#v verified=%v", proof, verified)
	}

	freshTime := configTime.Add(time.Second)
	if err := RecordRuntimeProof(root, codexAdapterID, "task.completed", freshTime); err != nil {
		t.Fatal(err)
	}
	proof, verified = runtimeProofAfterConfig(root, codexAdapterID, configPath)
	if !verified || !proof.LastSeen.Equal(freshTime) {
		t.Fatalf("fresh proof was rejected: %#v verified=%v", proof, verified)
	}
	if _, verified := runtimeEventProofAfterConfig(
		root,
		codexAdapterID,
		"approval.required",
		configPath,
	); verified {
		t.Fatal("stale approval event proof was accepted")
	}
	eventProof, verified := runtimeEventProofAfterConfig(
		root,
		codexAdapterID,
		"task.completed",
		configPath,
	)
	if !verified || !eventProof.LastSeen.Equal(freshTime) {
		t.Fatalf("fresh completion proof was rejected: %#v verified=%v", eventProof, verified)
	}
}

func TestRuntimeProofRejectsUnsupportedOrInvalidData(t *testing.T) {
	root := t.TempDir()
	if err := RecordRuntimeProof(root, "unknown", "task.completed", time.Now()); err == nil {
		t.Fatal("expected unsupported adapter error")
	}
	if err := RecordRuntimeProof(root, codexAdapterID, "", time.Now()); err == nil {
		t.Fatal("expected empty event error")
	}
	path := runtimeProofPath(root, kimiAdapterID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"version":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readRuntimeProof(root, kimiAdapterID); err == nil {
		t.Fatal("expected invalid proof error")
	}
}
