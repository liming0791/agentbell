package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

var ErrNotFound = errors.New("agentbell settings not found")

const maximumSettingsBytes = 1 << 20

func Load(path string) (Settings, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return Settings{}, ErrNotFound
	}
	if err != nil {
		return Settings{}, err
	}
	defer file.Close()

	raw, err := io.ReadAll(io.LimitReader(file, maximumSettingsBytes+1))
	if err != nil {
		return Settings{}, err
	}
	if len(raw) > maximumSettingsBytes {
		return Settings{}, fmt.Errorf(
			"settings exceed %d bytes",
			maximumSettingsBytes,
		)
	}
	if err := validateDocumentShape(raw); err != nil {
		return Settings{}, fmt.Errorf("parse settings: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var result Settings
	if err := decoder.Decode(&result); err != nil {
		return Settings{}, fmt.Errorf("parse settings: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Settings{}, errors.New("parse settings: trailing JSON value")
		}
		return Settings{}, fmt.Errorf("parse settings: trailing data: %w", err)
	}
	if err := result.Validate(); err != nil {
		return Settings{}, fmt.Errorf("validate settings: %w", err)
	}
	return result, nil
}

// Save validates settings and publishes a complete JSON document with an
// atomic rename. The containing directory and file use private permissions on
// platforms that implement Unix permission bits.
func Save(path string, value *Settings) error {
	if value == nil {
		return errors.New("settings is nil")
	}
	if err := value.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > maximumSettingsBytes {
		return fmt.Errorf("settings exceed %d bytes", maximumSettingsBytes)
	}

	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".settings-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}
	if _, err := temporary.Write(data); err != nil {
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
	if err := os.Chmod(temporaryPath, 0o600); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	return nil
}

func validateDocumentShape(raw []byte) error {
	if err := rejectDuplicateSettingsKeys(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok || delimiter != '{' {
		return errors.New("settings must be an object")
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		name, ok := token.(string)
		if !ok {
			return errors.New("settings field name must be a string")
		}
		if _, exists := fields[name]; exists {
			return fmt.Errorf("duplicate field %q", name)
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
		fields[name] = value
	}
	if _, err := decoder.Token(); err != nil {
		return err
	}
	for _, name := range []string{
		"version",
		"minCoreVersion",
		"events",
		"defaultTemplate",
		"templates",
		"quietHours",
		"policies",
	} {
		value, exists := fields[name]
		if !exists {
			return fmt.Errorf("required field %q is missing", name)
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("required field %q cannot be null", name)
		}
	}
	for name, rawValue := range fields {
		var value any
		if err := json.Unmarshal(rawValue, &value); err != nil {
			return fmt.Errorf("field %q: %w", name, err)
		}
		if err := rejectNullValues(value, name); err != nil {
			return err
		}
	}
	return nil
}

func rejectDuplicateSettingsKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	return scanSettingsJSONValue(decoder, "")
}

func scanSettingsJSONValue(
	decoder *json.Decoder,
	path string,
) error {
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
		seen := make(map[string]bool)
		for decoder.More() {
			token, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := token.(string)
			if !ok {
				return errors.New("settings field name must be a string")
			}
			if seen[name] {
				if path == "events" {
					return fmt.Errorf("duplicate event %q", name)
				}
				return fmt.Errorf("duplicate field %q", name)
			}
			seen[name] = true
			childPath := name
			if path != "" {
				childPath = path + "." + name
			}
			if err := scanSettingsJSONValue(decoder, childPath); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := scanSettingsJSONValue(decoder, path+"[]"); err != nil {
				return err
			}
		}
		_, err = decoder.Token()
		return err
	default:
		return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
}

func rejectNullValues(value any, path string) error {
	switch typed := value.(type) {
	case nil:
		return fmt.Errorf("field %q cannot be null", path)
	case map[string]any:
		for name, child := range typed {
			if err := rejectNullValues(child, path+"."+name); err != nil {
				return err
			}
		}
	case []any:
		for index, child := range typed {
			if err := rejectNullValues(
				child,
				fmt.Sprintf("%s[%d]", path, index),
			); err != nil {
				return err
			}
		}
	}
	return nil
}
