package relay

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func FuzzRelayEnvelopeDecode(f *testing.F) {
	valid, err := os.ReadFile(filepath.Join(
		"..",
		"..",
		"testdata",
		"relay-envelope.golden.json",
	))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid)
	f.Add(append(append([]byte(nil), valid...), []byte(` {}`)...))
	f.Add(append(valid[:len(valid)-2], []byte(`,"unknown":true}`)...))
	f.Add(bytes.Repeat([]byte("x"), MaxBodyBytes+1))
	f.Add([]byte{})
	f.Add([]byte{0xff, 0x00, '{', '}'})

	f.Fuzz(func(t *testing.T, raw []byte) {
		envelope, err := Decode(raw)
		if err != nil {
			return
		}
		if err := envelope.Validate(); err != nil {
			t.Fatal("successful relay decode violated Validate")
		}
		if len(raw) == 0 || len(raw) > MaxBodyBytes {
			t.Fatal("successful relay decode violated body bounds")
		}
		if envelope.Event.PrivacyLevel == "metadata-only" &&
			(envelope.Event.CWD != "" || envelope.Event.Summary != "") {
			t.Fatal("relay decode violated metadata-only privacy")
		}
		encoded, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal("successful relay envelope could not be encoded")
		}
		second, err := Decode(encoded)
		if err != nil {
			t.Fatal("encoded relay envelope could not be decoded")
		}
		if second.Delivery.Key != envelope.Delivery.Key ||
			second.Delivery.ProducerKey != envelope.Delivery.ProducerKey ||
			second.TeamID != envelope.TeamID ||
			second.Origin != envelope.Origin ||
			!second.SentAt.Equal(envelope.SentAt) {
			t.Fatal("relay envelope round trip changed transport identity")
		}
	})
}
