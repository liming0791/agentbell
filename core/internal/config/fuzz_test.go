package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestConfigLoadMatchesTransactionStrictness(t *testing.T) {
	valid := `{
		"defaultChannel":"primary",
		"notifications":{"events":[],"includeSummary":false},
		"channels":[{"id":"primary","chatId":"oc_primary"}]
	}`
	tests := map[string]string{
		"trailing JSON": valid + ` {}`,
		"duplicate top-level": `{
			"defaultChannel":"primary",
			"defaultChannel":"primary",
			"notifications":{"events":[],"includeSummary":false},
			"channels":[{"id":"primary","chatId":"oc_primary"}]
		}`,
		"duplicate nested": `{
			"defaultChannel":"primary",
			"notifications":{
				"events":[],
				"includeSummary":false,
				"includeSummary":true
			},
			"channels":[{"id":"primary","chatId":"oc_primary"}]
		}`,
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeConfig([]byte(raw)); err == nil {
				t.Fatal("transaction config decoder accepted ambiguous JSON")
			}
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("config loader accepted JSON rejected by transaction decoder")
			}
		})
	}
}

func FuzzConfigStrictDecode(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte(`{
			"defaultChannel":"primary",
			"notifications":{"events":[],"includeSummary":false},
			"channels":[{"id":"primary","chatId":"oc_legacy"}]
		}`),
		[]byte(`{
			"defaultChannel":"primary",
			"notifications":{
				"events":["task.completed"],
				"includeSummary":false,
				"privacyLevel":"metadata-only"
			},
			"channels":[{
				"id":"primary",
				"name":"Primary",
				"type":"feishu",
				"chatId":"oc_m2",
				"as":"bot"
			}]
		}`),
		[]byte(`{"unknown":true}`),
		[]byte(`{"defaultChannel":"primary"} {}`),
		[]byte(`{
			"defaultChannel":"primary",
			"defaultChannel":"primary",
			"notifications":{"events":[],"includeSummary":false},
			"channels":[{"id":"primary","chatId":"oc_primary"}]
		}`),
		[]byte(`{
			"defaultChannel":"primary",
			"notifications":{"events":[],"events":[],"includeSummary":false},
			"channels":[{"id":"primary","chatId":"oc_primary"}]
		}`),
		[]byte{},
		[]byte{0xff, 0x00, '{', '}'},
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > maximumConfigSize {
			return
		}
		first, err := decodeConfig(raw)
		if err != nil {
			return
		}
		if err := first.Validate(); err != nil {
			t.Fatal("successful config decode violated Validate")
		}
		encoded, err := encodeConfig(first)
		if err != nil {
			t.Fatal("successful config could not be encoded")
		}
		second, err := decodeConfig(encoded)
		if err != nil {
			t.Fatal("encoded config could not be decoded")
		}
		if !reflect.DeepEqual(first, second) {
			t.Fatal("config round trip changed the accepted value")
		}
		if _, found := second.Default(); !found {
			t.Fatal("accepted config lost its default channel")
		}
	})
}
