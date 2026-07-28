package remoteconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"testing"
)

func FuzzRemoteSidecarStrictDecode(f *testing.F) {
	remoteSeed, err := json.Marshal(validRemote(validWSLConnector()))
	if err != nil {
		f.Fatal(err)
	}
	relaySeed, err := json.Marshal(validRelay())
	if err != nil {
		f.Fatal(err)
	}
	f.Add(uint8(0), remoteSeed)
	f.Add(uint8(1), relaySeed)
	f.Add(uint8(0), []byte(`{"version":1,"version":2}`))
	f.Add(uint8(1), append(append([]byte(nil), relaySeed...), []byte(` {}`)...))
	f.Add(uint8(0), []byte(`{"version":2,"unknown":"private"}`))
	f.Add(uint8(1), []byte{0xff, 0x00, '{', '}'})

	f.Fuzz(func(t *testing.T, kind uint8, raw []byte) {
		if len(raw) > maximumSidecarBytes {
			return
		}
		if kind%2 == 0 {
			first, err := decodeFuzzRemote(raw)
			if err != nil {
				return
			}
			if err := first.Validate(); err != nil {
				t.Fatal("successful remote sidecar decode violated Validate")
			}
			encoded, err := json.Marshal(first)
			if err != nil {
				t.Fatal("successful remote sidecar could not be encoded")
			}
			second, err := decodeFuzzRemote(encoded)
			if err != nil || !reflect.DeepEqual(first, second) {
				t.Fatal("remote sidecar round trip was not stable")
			}
			return
		}

		first, err := decodeFuzzRelay(raw)
		if err != nil {
			return
		}
		if err := first.Validate(); err != nil {
			t.Fatal("successful relay sidecar decode violated Validate")
		}
		encoded, err := json.Marshal(first)
		if err != nil {
			t.Fatal("successful relay sidecar could not be encoded")
		}
		second, err := decodeFuzzRelay(encoded)
		if err != nil || !reflect.DeepEqual(first, second) {
			t.Fatal("relay sidecar round trip was not stable")
		}
	})
}

func decodeFuzzRemote(raw []byte) (RemoteConfig, error) {
	var result RemoteConfig
	if err := decodeFuzzSidecar(raw, &result, validateRemoteShape); err != nil {
		return RemoteConfig{}, err
	}
	if err := result.Validate(); err != nil {
		return RemoteConfig{}, err
	}
	return result, nil
}

func decodeFuzzRelay(raw []byte) (RelayConfig, error) {
	var result RelayConfig
	if err := decodeFuzzSidecar(raw, &result, validateRelayShape); err != nil {
		return RelayConfig{}, err
	}
	if err := result.Validate(); err != nil {
		return RelayConfig{}, err
	}
	return result, nil
}

func decodeFuzzSidecar(
	raw []byte,
	destination any,
	validateShape func([]byte) error,
) error {
	if len(raw) == 0 || len(raw) > maximumSidecarBytes {
		return errors.New("sidecar size is invalid")
	}
	if err := rejectDuplicateObjectKeys(raw); err != nil {
		return err
	}
	if err := validateShape(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("sidecar contains trailing data")
	}
	return nil
}
