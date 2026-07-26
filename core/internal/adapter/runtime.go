package adapter

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

type runtimeProof struct {
	Version  int       `json:"version"`
	Adapter  string    `json:"adapter"`
	Event    string    `json:"event,omitempty"`
	LastSeen time.Time `json:"lastSeen"`
}

// RecordRuntimeProof records only that an installed hook reached the Core.
// It contains the adapter/event names but no raw payload or session/task/turn
// identifiers.
func RecordRuntimeProof(stateDir, adapterID, eventName string, seenAt time.Time) error {
	if adapterID != codexAdapterID &&
		adapterID != claudeAdapterID &&
		adapterID != kimiAdapterID {
		return errors.New("unsupported adapter runtime proof")
	}
	if eventName == "" {
		return errors.New("runtime proof event is required")
	}
	proof := runtimeProof{
		Version:  2,
		Adapter:  adapterID,
		Event:    eventName,
		LastSeen: seenAt.UTC(),
	}
	// Event proofs live in separate files so concurrent Hook types cannot
	// overwrite one another's evidence.
	if err := writeJSONFile(runtimeEventProofPath(stateDir, adapterID, eventName), proof); err != nil {
		return err
	}
	return writeJSONFile(runtimeProofPath(stateDir, adapterID), proof)
}

func runtimeProofPath(stateDir, adapterID string) string {
	return filepath.Join(stateDir, "adapters", adapterID, "runtime-proof.json")
}

func runtimeEventProofPath(stateDir, adapterID, eventName string) string {
	sum := sha256.Sum256([]byte(eventName))
	name := hex.EncodeToString(sum[:8]) + ".json"
	return filepath.Join(stateDir, "adapters", adapterID, "runtime-proof-events", name)
}

func readRuntimeProof(stateDir, adapterID string) (runtimeProof, error) {
	return readRuntimeProofPath(runtimeProofPath(stateDir, adapterID), adapterID, "")
}

func readRuntimeEventProof(stateDir, adapterID, eventName string) (runtimeProof, error) {
	return readRuntimeProofPath(
		runtimeEventProofPath(stateDir, adapterID, eventName),
		adapterID,
		eventName,
	)
}

func readRuntimeProofPath(path, adapterID, eventName string) (runtimeProof, error) {
	value, err := os.ReadFile(path)
	if err != nil {
		return runtimeProof{}, err
	}
	var proof runtimeProof
	if err := json.Unmarshal(value, &proof); err != nil {
		return runtimeProof{}, err
	}
	if (proof.Version != 1 && proof.Version != 2) ||
		proof.Adapter != adapterID ||
		proof.LastSeen.IsZero() ||
		(eventName != "" && (proof.Version != 2 || proof.Event != eventName)) {
		return runtimeProof{}, errors.New("invalid adapter runtime proof")
	}
	return proof, nil
}

func runtimeProofAfterConfig(stateDir, adapterID, configPath string) (runtimeProof, bool) {
	proof, err := readRuntimeProof(stateDir, adapterID)
	if err != nil {
		return runtimeProof{}, false
	}
	info, err := os.Stat(configPath)
	if err != nil {
		return proof, false
	}
	return proof, !proof.LastSeen.Before(info.ModTime())
}

func runtimeEventProofAfterConfig(
	stateDir, adapterID, eventName, configPath string,
) (runtimeProof, bool) {
	proof, err := readRuntimeEventProof(stateDir, adapterID, eventName)
	if err != nil {
		return runtimeProof{}, false
	}
	info, err := os.Stat(configPath)
	if err != nil {
		return proof, false
	}
	return proof, !proof.LastSeen.Before(info.ModTime())
}
