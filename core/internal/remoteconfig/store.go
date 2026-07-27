package remoteconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

const maximumSidecarBytes = 1 << 20

func LoadRemote(path string) (RemoteConfig, error) {
	var result RemoteConfig
	if err := load(path, &result, validateRemoteShape); err != nil {
		return RemoteConfig{}, err
	}
	if err := result.Validate(); err != nil {
		return RemoteConfig{}, fmt.Errorf("validate remote.json: %w", err)
	}
	return result, nil
}

func SaveRemote(path string, value *RemoteConfig) error {
	if value == nil {
		return errors.New("remote config is nil")
	}
	if err := value.Validate(); err != nil {
		return err
	}
	return save(path, value)
}

func LoadRelay(path string) (RelayConfig, error) {
	var result RelayConfig
	if err := load(path, &result, validateRelayShape); err != nil {
		return RelayConfig{}, err
	}
	if err := result.Validate(); err != nil {
		return RelayConfig{}, fmt.Errorf("validate relay.json: %w", err)
	}
	return result, nil
}

func SaveRelay(path string, value *RelayConfig) error {
	if value == nil {
		return errors.New("relay config is nil")
	}
	if err := value.Validate(); err != nil {
		return err
	}
	return save(path, value)
}

func load(
	path string,
	destination any,
	validateShape func([]byte) error,
) error {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %s", ErrNotFound, path)
	}
	if err != nil {
		return err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maximumSidecarBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > maximumSidecarBytes {
		return fmt.Errorf("sidecar exceeds %d bytes", maximumSidecarBytes)
	}
	if err := rejectDuplicateObjectKeys(raw); err != nil {
		return fmt.Errorf("parse sidecar: %w", err)
	}
	if err := validateShape(raw); err != nil {
		return fmt.Errorf("parse sidecar: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("parse sidecar: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("parse sidecar: trailing JSON value")
		}
		return fmt.Errorf("parse sidecar trailing data: %w", err)
	}
	return nil
}

func save(path string, value any) error {
	if path == "" {
		return errors.New("sidecar path is required")
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if len(encoded) > maximumSidecarBytes {
		return fmt.Errorf("sidecar exceeds %d bytes", maximumSidecarBytes)
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := setSidecarPermissions(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".agentbell-sidecar-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}
	if err := setSidecarFilePermissions(temporary, 0o600); err != nil {
		cleanup()
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		cleanup()
		return err
	}
	if err := temporary.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	if err := setSidecarPermissions(path, 0o600); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	handle, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}

func validateRemoteShape(raw []byte) error {
	root, err := rawObject(raw)
	if err != nil {
		return err
	}
	return requireFields(root, []string{
		"version",
		"minCoreVersion",
		"teamId",
		"originId",
		"runtime",
		"outbox",
		"connector",
		"privateKeyRef",
	})
}

func validateRelayShape(raw []byte) error {
	root, err := rawObject(raw)
	if err != nil {
		return err
	}
	if err := requireFields(root, []string{
		"version",
		"minCoreVersion",
		"listener",
		"peers",
	}); err != nil {
		return err
	}
	var listener map[string]json.RawMessage
	if err := json.Unmarshal(root["listener"], &listener); err != nil || listener == nil {
		return errors.New("field \"listener\" must be an object")
	}
	if err := requireFields(listener, []string{"enabled"}); err != nil {
		return fmt.Errorf("listener: %w", err)
	}
	var peers []map[string]json.RawMessage
	if err := json.Unmarshal(root["peers"], &peers); err != nil || peers == nil {
		return errors.New("field \"peers\" must be an array")
	}
	for index, peer := range peers {
		if peer == nil {
			return fmt.Errorf("peers[%d] must be an object", index)
		}
		if err := requireFields(peer, []string{
			"id",
			"teamId",
			"originId",
			"publicKey",
			"scopes",
			"allowedSources",
			"allowedRuntimes",
			"revoked",
		}); err != nil {
			return fmt.Errorf("peers[%d]: %w", index, err)
		}
	}
	return nil
}

func rawObject(raw []byte) (map[string]json.RawMessage, error) {
	var result map[string]json.RawMessage
	if err := json.Unmarshal(raw, &result); err != nil || result == nil {
		return nil, errors.New("sidecar root must be an object")
	}
	return result, nil
}

func requireFields(
	object map[string]json.RawMessage,
	fields []string,
) error {
	for _, field := range fields {
		value, exists := object[field]
		if !exists {
			return fmt.Errorf("required field %q is missing", field)
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("required field %q cannot be null", field)
		}
	}
	return nil
}

func rejectDuplicateObjectKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	return scanJSONValue(decoder)
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			token, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := token.(string)
			if !ok {
				return errors.New("object key must be a string")
			}
			if seen[name] {
				return fmt.Errorf("duplicate field %q", name)
			}
			seen[name] = true
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
}
