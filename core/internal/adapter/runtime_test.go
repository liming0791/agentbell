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

func TestRuntimeProofAcceptsM15Adapters(t *testing.T) {
	root := t.TempDir()
	for _, adapterID := range []string{qoderWorkAdapterID, traeAdapterID} {
		if err := RecordRuntimeProof(
			root,
			adapterID,
			"task.completed",
			time.Now(),
		); err != nil {
			t.Fatalf("%s runtime proof: %v", adapterID, err)
		}
	}
}

func TestRuntimeProofMatchesActivationGeneration(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "hooks.json")
	configTime := time.Date(2026, 7, 27, 8, 0, 0, 0, time.UTC)
	if err := os.WriteFile(configPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(configPath, configTime, configTime); err != nil {
		t.Fatal(err)
	}
	context := RuntimeProofContext{
		BridgeProtocol:       1,
		CoreVersion:          "0.3.0",
		ActivationGeneration: 7,
	}
	if err := RecordRuntimeProofWithContext(
		root,
		codexAdapterID,
		"task.completed",
		configTime.Add(time.Second),
		context,
	); err != nil {
		t.Fatal(err)
	}
	proof, verified := runtimeEventProofAfterConfigAndGeneration(
		root,
		codexAdapterID,
		"task.completed",
		configPath,
		7,
	)
	if !verified ||
		proof.Version != 3 ||
		proof.BridgeProtocol != 1 ||
		proof.CoreVersion != "0.3.0" ||
		proof.ActivationGeneration != 7 {
		t.Fatalf("bridge proof was rejected: %#v verified=%v", proof, verified)
	}
	if _, verified := runtimeEventProofAfterConfigAndGeneration(
		root,
		codexAdapterID,
		"task.completed",
		configPath,
		8,
	); verified {
		t.Fatal("proof from another activation generation was accepted")
	}
}

func TestRuntimeProofRejectsIncompleteBridgeContext(t *testing.T) {
	root := t.TempDir()
	tests := []RuntimeProofContext{
		{BridgeProtocol: 1, CoreVersion: "0.3.0"},
		{BridgeProtocol: 1, ActivationGeneration: 1},
		{BridgeProtocol: 2, CoreVersion: "0.3.0", ActivationGeneration: 1},
		{CoreVersion: "0.3.0", ActivationGeneration: 1},
	}
	for _, context := range tests {
		if err := RecordRuntimeProofWithContext(
			root,
			codexAdapterID,
			"task.completed",
			time.Now(),
			context,
		); err == nil {
			t.Fatalf("invalid bridge context was accepted: %#v", context)
		}
	}
}
