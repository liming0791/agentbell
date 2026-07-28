package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func FuzzSettingsSidecarStrictDecode(f *testing.F) {
	valid, err := json.Marshal(validSettings())
	if err != nil {
		f.Fatal(err)
	}
	nestedDuplicateQuietHours := []byte(strings.Replace(
		string(valid),
		`"enabled":true`,
		`"enabled":true,"enabled":true`,
		1,
	))
	nestedDuplicateTemplate := []byte(strings.Replace(
		string(valid),
		`"id":"standard"`,
		`"id":"standard","id":"standard"`,
		1,
	))
	for _, seed := range [][]byte{
		valid,
		[]byte(`{"version":2}`),
		[]byte(`{"version":1,"unknown":true}`),
		append(append([]byte(nil), valid...), []byte(` {}`)...),
		[]byte(`{"version":1,"version":1}`),
		nestedDuplicateQuietHours,
		nestedDuplicateTemplate,
		[]byte(`null`),
		[]byte{0xff, 0x00, '[', ']'},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > maximumSettingsBytes {
			return
		}
		first, err := decodeFuzzSettings(raw)
		if err != nil {
			return
		}
		if err := first.Validate(); err != nil {
			t.Fatal("successful settings decode violated Validate")
		}
		encoded, err := json.Marshal(first)
		if err != nil {
			t.Fatal("successful settings could not be encoded")
		}
		second, err := decodeFuzzSettings(encoded)
		if err != nil {
			t.Fatal("encoded settings could not be decoded")
		}
		if !reflect.DeepEqual(first, second) {
			t.Fatal("settings round trip changed the accepted value")
		}
	})
}

func TestSettingsLoadRejectsNestedDuplicateFields(t *testing.T) {
	valid, err := json.Marshal(validSettings())
	if err != nil {
		t.Fatal(err)
	}
	candidates := map[string][]byte{
		"quiet hours": []byte(strings.Replace(
			string(valid),
			`"enabled":true`,
			`"enabled":true,"enabled":true`,
			1,
		)),
		"template": []byte(strings.Replace(
			string(valid),
			`"id":"standard"`,
			`"id":"standard","id":"standard"`,
			1,
		)),
	}
	for name, raw := range candidates {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeFuzzSettings(raw); err == nil ||
				!strings.Contains(err.Error(), "duplicate") {
				t.Fatalf("strict decode accepted nested duplicate field: %v", err)
			}

			path := filepath.Join(t.TempDir(), "settings.json")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil ||
				!strings.Contains(err.Error(), "duplicate") {
				t.Fatalf("Load accepted nested duplicate field: %v", err)
			}
		})
	}
}

func decodeFuzzSettings(raw []byte) (Settings, error) {
	if len(raw) == 0 || len(raw) > maximumSettingsBytes {
		return Settings{}, errors.New("settings size is invalid")
	}
	if err := validateDocumentShape(raw); err != nil {
		return Settings{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var result Settings
	if err := decoder.Decode(&result); err != nil {
		return Settings{}, err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Settings{}, errors.New("settings contains trailing data")
	}
	if err := result.Validate(); err != nil {
		return Settings{}, err
	}
	return result, nil
}
