package adapter

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type runtimeProof struct {
	Version              int       `json:"version"`
	Adapter              string    `json:"adapter"`
	Event                string    `json:"event,omitempty"`
	LastSeen             time.Time `json:"lastSeen"`
	BridgeProtocol       int       `json:"bridgeProtocol,omitempty"`
	CoreVersion          string    `json:"coreVersion,omitempty"`
	ActivationGeneration uint64    `json:"activationGeneration,omitempty"`
}

type RuntimeProofContext struct {
	BridgeProtocol       int
	CoreVersion          string
	ActivationGeneration uint64
}

// RecordRuntimeProof records only that an installed hook reached the Core.
// It contains the adapter/event names but no raw payload or session/task/turn
// identifiers.
func RecordRuntimeProof(stateDir, adapterID, eventName string, seenAt time.Time) error {
	return RecordRuntimeProofWithContext(
		stateDir,
		adapterID,
		eventName,
		seenAt,
		RuntimeProofContext{},
	)
}

func RecordRuntimeProofWithContext(
	stateDir,
	adapterID,
	eventName string,
	seenAt time.Time,
	context RuntimeProofContext,
) error {
	if adapterID != codexAdapterID &&
		adapterID != claudeAdapterID &&
		adapterID != kimiAdapterID &&
		adapterID != opencodeAdapterID &&
		adapterID != qoderAdapterID &&
		adapterID != qoderWorkAdapterID &&
		adapterID != traeAdapterID {
		return errors.New("unsupported adapter runtime proof")
	}
	if eventName == "" {
		return errors.New("runtime proof event is required")
	}
	if err := context.validate(); err != nil {
		return err
	}
	proofVersion := 2
	if context.BridgeProtocol != 0 {
		proofVersion = 3
	}
	proof := runtimeProof{
		Version:              proofVersion,
		Adapter:              adapterID,
		Event:                eventName,
		LastSeen:             seenAt.UTC(),
		BridgeProtocol:       context.BridgeProtocol,
		CoreVersion:          context.CoreVersion,
		ActivationGeneration: context.ActivationGeneration,
	}
	// Event proofs live in separate files so concurrent Hook types cannot
	// overwrite one another's evidence.
	if err := writeJSONFile(runtimeEventProofPath(stateDir, adapterID, eventName), proof); err != nil {
		return err
	}
	return writeJSONFile(runtimeProofPath(stateDir, adapterID), proof)
}

func (context RuntimeProofContext) validate() error {
	if context.BridgeProtocol == 0 &&
		context.CoreVersion == "" &&
		context.ActivationGeneration == 0 {
		return nil
	}
	if context.BridgeProtocol != 1 {
		return errors.New("runtime proof bridge protocol must be 1")
	}
	if context.ActivationGeneration == 0 {
		return errors.New("runtime proof activation generation is required")
	}
	if strings.TrimSpace(context.CoreVersion) == "" ||
		len(context.CoreVersion) > 128 ||
		strings.ContainsAny(context.CoreVersion, "\x00\r\n") {
		return errors.New("runtime proof Core version is invalid")
	}
	return nil
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
	if (proof.Version != 1 && proof.Version != 2 && proof.Version != 3) ||
		proof.Adapter != adapterID ||
		proof.LastSeen.IsZero() ||
		(eventName != "" && (proof.Version < 2 || proof.Event != eventName)) {
		return runtimeProof{}, errors.New("invalid adapter runtime proof")
	}
	context := RuntimeProofContext{
		BridgeProtocol:       proof.BridgeProtocol,
		CoreVersion:          proof.CoreVersion,
		ActivationGeneration: proof.ActivationGeneration,
	}
	if proof.Version == 3 {
		if err := context.validate(); err != nil {
			return runtimeProof{}, errors.New("invalid adapter runtime proof")
		}
	} else if proof.BridgeProtocol != 0 ||
		proof.CoreVersion != "" ||
		proof.ActivationGeneration != 0 {
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

func runtimeEventProofAfterConfigAndGeneration(
	stateDir,
	adapterID,
	eventName,
	configPath string,
	generation uint64,
) (runtimeProof, bool) {
	if generation == 0 {
		return runtimeProof{}, false
	}
	proof, verified := runtimeEventProofAfterConfig(
		stateDir,
		adapterID,
		eventName,
		configPath,
	)
	if !verified ||
		proof.Version != 3 ||
		proof.ActivationGeneration != generation {
		return proof, false
	}
	return proof, true
}

func runtimeProofAfterConfigAndGeneration(
	stateDir,
	adapterID,
	configPath string,
	generation uint64,
) (runtimeProof, bool) {
	if generation == 0 {
		return runtimeProof{}, false
	}
	proof, verified := runtimeProofAfterConfig(stateDir, adapterID, configPath)
	if !verified ||
		proof.Version != 3 ||
		proof.BridgeProtocol != stableBridgeProtocol ||
		proof.ActivationGeneration != generation {
		return proof, false
	}
	return proof, true
}
