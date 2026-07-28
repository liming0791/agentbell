package relay

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/liming0791/agentbell/core/internal/event"
)

func validEnvelope(t testing.TB) Envelope {
	t.Helper()
	sentAt := time.Date(2026, 7, 27, 1, 2, 3, 456000000, time.UTC)
	notification := event.Notification{
		Version:        event.Version,
		Source:         "codex",
		Surface:        "cli",
		Runtime:        "wsl",
		Event:          event.EventTaskCompleted,
		Status:         event.StatusCompleted,
		OccurredAt:     sentAt.Add(-time.Second),
		SessionID:      "sha256:0123456789abcdef",
		IdempotencyKey: "sha256:" + strings.Repeat("a", 64),
		Priority:       event.PriorityNormal,
		PrivacyLevel:   event.PrivacyMetadataOnly,
		Project:        "agentbell",
	}
	key, err := DeriveDeliveryKey("team-main", "origin-wsl-ubuntu", notification.IdempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	return Envelope{
		ProtocolVersion: ProtocolVersion,
		TeamID:          "team-main",
		Origin: Origin{
			ID:      "origin-wsl-ubuntu",
			Runtime: "wsl",
		},
		Delivery: Delivery{
			Key:         key,
			ProducerKey: notification.IdempotencyKey,
		},
		SentAt: sentAt,
		Nonce:  "0123456789abcdef0123456789abcdef",
		Hop:    0,
		Event:  notification,
	}
}

func encodedEnvelope(t testing.TB, envelope Envelope) []byte {
	t.Helper()
	value, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestDecodeStrictEnvelope(t *testing.T) {
	value := encodedEnvelope(t, validEnvelope(t))
	decoded, err := Decode(value)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ProtocolVersion != ProtocolVersion ||
		decoded.Origin.ID != "origin-wsl-ubuntu" ||
		decoded.Event.Runtime != "wsl" {
		t.Fatalf("unexpected envelope: %#v", decoded)
	}

	withUnknown := append(value[:len(value)-1], []byte(`,"unknown":true}`)...)
	if _, err := Decode(withUnknown); err == nil {
		t.Fatal("unknown fields must be rejected")
	}
	if _, err := Decode(append(value, []byte(` {}`)...)); err == nil {
		t.Fatal("trailing JSON must be rejected")
	}
}

func TestGoldenRelayEnvelope(t *testing.T) {
	value, err := os.ReadFile(filepath.Join(
		"..",
		"..",
		"testdata",
		"relay-envelope.golden.json",
	))
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := Decode(value)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.TeamID != "team-main" ||
		envelope.Origin.ID != "origin-wsl-ubuntu" ||
		envelope.Event.Event != event.EventTaskCompleted {
		t.Fatalf("unexpected golden envelope: %#v", envelope)
	}
}

func TestDecodeRejectsBodyOverLimit(t *testing.T) {
	value := append(encodedEnvelope(t, validEnvelope(t)), make([]byte, MaxBodyBytes)...)
	if _, err := Decode(value); err == nil {
		t.Fatal("oversized body must be rejected")
	}
	if _, err := Decode(nil); err == nil {
		t.Fatal("empty body must be rejected")
	}
	if _, err := Decode([]byte(`{"protocolVersion":`)); err == nil {
		t.Fatal("malformed JSON must be rejected")
	}
	if _, err := Decode(append(encodedEnvelope(t, validEnvelope(t)), '!')); err == nil {
		t.Fatal("invalid trailing data must be rejected")
	}
}

func TestEnvelopeValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Envelope)
	}{
		{"protocol", func(value *Envelope) { value.ProtocolVersion = "2" }},
		{"team", func(value *Envelope) { value.TeamID = "../team" }},
		{"origin", func(value *Envelope) { value.Origin.ID = "" }},
		{"origin runtime", func(value *Envelope) { value.Origin.Runtime = "host" }},
		{"delivery key", func(value *Envelope) { value.Delivery.Key = "sha256:bad" }},
		{"producer key", func(value *Envelope) { value.Delivery.ProducerKey = "different" }},
		{"sentAt", func(value *Envelope) { value.SentAt = time.Time{} }},
		{"nonce uppercase", func(value *Envelope) { value.Nonce = strings.Repeat("A", 32) }},
		{"nonce length", func(value *Envelope) { value.Nonce = "abcd" }},
		{"negative hop", func(value *Envelope) { value.Hop = -1 }},
		{"hop overflow", func(value *Envelope) { value.Hop = MaxHop + 1 }},
		{"event", func(value *Envelope) { value.Event.Event = "made.up" }},
		{"event version", func(value *Envelope) { value.Event.Version = "2" }},
		{"event source", func(value *Envelope) { value.Event.Source = "unknown" }},
		{"event surface", func(value *Envelope) { value.Event.Surface = "unknown" }},
		{"event status", func(value *Envelope) { value.Event.Status = "unknown" }},
		{"event occurredAt", func(value *Envelope) { value.Event.OccurredAt = time.Time{} }},
		{"event idempotency", func(value *Envelope) {
			value.Event.IdempotencyKey = ""
			value.Delivery.ProducerKey = ""
		}},
		{"event priority", func(value *Envelope) { value.Event.Priority = "unknown" }},
		{"event privacy", func(value *Envelope) { value.Event.PrivacyLevel = "unknown" }},
		{"event summary length", func(value *Envelope) {
			value.Event.PrivacyLevel = event.PrivacySummary
			value.Event.Summary = strings.Repeat("x", 301)
		}},
		{"metadata privacy", func(value *Envelope) { value.Event.Summary = "secret" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := validEnvelope(t)
			test.mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestDeliveryKeyIsStableAndScoped(t *testing.T) {
	producer := "sha256:" + strings.Repeat("b", 64)
	first, err := DeriveDeliveryKey("team-a", "origin-a", producer)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := DeriveDeliveryKey("team-a", "origin-a", producer)
	otherTeam, _ := DeriveDeliveryKey("team-b", "origin-a", producer)
	otherOrigin, _ := DeriveDeliveryKey("team-a", "origin-b", producer)
	if first != second {
		t.Fatalf("delivery key is unstable: %q != %q", first, second)
	}
	if first == otherTeam || first == otherOrigin || otherTeam == otherOrigin {
		t.Fatal("team and origin must isolate delivery keys")
	}
	if _, err := DeriveDeliveryKey("", "origin-a", producer); err == nil {
		t.Fatal("empty team must fail")
	}
}

func TestSignAndVerifyUsesExactBody(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	envelope := validEnvelope(t)
	body := encodedEnvelope(t, envelope)
	signature, err := Sign(
		privateKey,
		"POST",
		"/v1/relay/events",
		envelope.SentAt,
		envelope.Nonce,
		body,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(
		publicKey,
		"POST",
		"/v1/relay/events",
		envelope.SentAt,
		envelope.Nonce,
		body,
		signature,
	); err != nil {
		t.Fatal(err)
	}
	// Whitespace preserves the decoded JSON value but changes the exact body.
	tampered := append(append([]byte(nil), body...), '\n')
	if err := Verify(
		publicKey,
		"POST",
		"/v1/relay/events",
		envelope.SentAt,
		envelope.Nonce,
		tampered,
		signature,
	); err == nil {
		t.Fatal("tampered exact body must fail verification")
	}
	if err := Verify(
		otherPublic,
		"POST",
		"/v1/relay/events",
		envelope.SentAt,
		envelope.Nonce,
		body,
		signature,
	); err == nil {
		t.Fatal("wrong public key must fail verification")
	}
	if _, err := Sign(
		ed25519.PrivateKey("short"),
		"POST",
		"/v1/relay/events",
		envelope.SentAt,
		envelope.Nonce,
		body,
	); err == nil {
		t.Fatal("invalid private key must fail")
	}
	if err := Verify(
		ed25519.PublicKey("short"),
		"POST",
		"/v1/relay/events",
		envelope.SentAt,
		envelope.Nonce,
		body,
		signature,
	); err == nil {
		t.Fatal("invalid public key must fail")
	}
	if err := Verify(
		publicKey,
		"POST",
		"/v1/relay/events",
		envelope.SentAt,
		envelope.Nonce,
		body,
		[]byte("short"),
	); err == nil {
		t.Fatal("invalid signature length must fail")
	}
}

func BenchmarkRelayVerifyPeer(b *testing.B) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		b.Fatal(err)
	}
	envelope := validEnvelope(b)
	body, err := json.Marshal(envelope)
	if err != nil {
		b.Fatal(err)
	}
	signature, err := Sign(
		privateKey,
		"POST",
		"/v1/relay/events",
		envelope.SentAt,
		envelope.Nonce,
		body,
	)
	if err != nil {
		b.Fatal(err)
	}
	peer := Peer{
		ID:              "peer-benchmark",
		TeamID:          envelope.TeamID,
		OriginID:        envelope.Origin.ID,
		PublicKey:       publicKey,
		Scopes:          []string{ScopeIngest},
		AllowedSources:  []string{envelope.Event.Source},
		AllowedRuntimes: []string{envelope.Event.Runtime},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		if _, err := VerifyPeer(
			peer,
			ScopeIngest,
			"POST",
			"/v1/relay/events",
			body,
			signature,
			envelope.SentAt,
			DefaultMaxSkew,
		); err != nil {
			b.Fatal(err)
		}
	}
}

func TestSigningMaterialValidation(t *testing.T) {
	envelope := validEnvelope(t)
	body := encodedEnvelope(t, envelope)
	tests := []struct {
		name   string
		method string
		target string
		sentAt time.Time
		nonce  string
		body   []byte
	}{
		{"method", "\n", "/v1/relay/events", envelope.SentAt, envelope.Nonce, body},
		{"target", "POST", "/v1/\nevents", envelope.SentAt, envelope.Nonce, body},
		{"sentAt", "POST", "/v1/relay/events", time.Time{}, envelope.Nonce, body},
		{"nonce", "POST", "/v1/relay/events", envelope.SentAt, "bad", body},
		{"empty body", "POST", "/v1/relay/events", envelope.SentAt, envelope.Nonce, nil},
		{
			"large body",
			"POST",
			"/v1/relay/events",
			envelope.SentAt,
			envelope.Nonce,
			make([]byte, MaxBodyBytes+1),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := SigningMaterial(
				test.method,
				test.target,
				test.sentAt,
				test.nonce,
				test.body,
			); err == nil {
				t.Fatal("expected signing material validation error")
			}
		})
	}
}

func TestVerifyPeerChecksScopeIdentityTimestampAndSignature(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	envelope := validEnvelope(t)
	body := encodedEnvelope(t, envelope)
	signature, err := Sign(
		privateKey,
		"POST",
		"/v1/relay/events",
		envelope.SentAt,
		envelope.Nonce,
		body,
	)
	if err != nil {
		t.Fatal(err)
	}
	peer := Peer{
		ID:              "peer-wsl",
		TeamID:          envelope.TeamID,
		OriginID:        envelope.Origin.ID,
		PublicKey:       publicKey,
		Scopes:          []string{ScopeIngest},
		AllowedSources:  []string{"codex"},
		AllowedRuntimes: []string{"wsl"},
	}
	if _, err := VerifyPeer(
		peer,
		ScopeIngest,
		"POST",
		"/v1/relay/events",
		body,
		signature,
		envelope.SentAt.Add(time.Minute),
		5*time.Minute,
	); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		peer   Peer
		now    time.Time
		scope  string
		mutate func(*Envelope)
	}{
		{"expired", peer, envelope.SentAt.Add(6 * time.Minute), ScopeIngest, nil},
		{"future", peer, envelope.SentAt.Add(-6 * time.Minute), ScopeIngest, nil},
		{"scope", peer, envelope.SentAt, "relay:admin", nil},
		{"revoked", func() Peer { value := peer; value.Revoked = true; return value }(), envelope.SentAt, ScopeIngest, nil},
		{"team", peer, envelope.SentAt, ScopeIngest, func(value *Envelope) {
			value.TeamID = "team-other"
			value.Delivery.Key, _ = DeriveDeliveryKey(
				value.TeamID,
				value.Origin.ID,
				value.Delivery.ProducerKey,
			)
		}},
		{"origin", peer, envelope.SentAt, ScopeIngest, func(value *Envelope) {
			value.Origin.ID = "origin-other"
			value.Delivery.Key, _ = DeriveDeliveryKey(
				value.TeamID,
				value.Origin.ID,
				value.Delivery.ProducerKey,
			)
		}},
		{"source", func() Peer {
			value := peer
			value.AllowedSources = []string{"claude"}
			return value
		}(), envelope.SentAt, ScopeIngest, nil},
		{"runtime", func() Peer {
			value := peer
			value.AllowedRuntimes = []string{"ssh"}
			return value
		}(), envelope.SentAt, ScopeIngest, nil},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := envelope
			if test.mutate != nil {
				test.mutate(&candidate)
			}
			candidateBody := encodedEnvelope(t, candidate)
			candidateSignature, signErr := Sign(
				privateKey,
				"POST",
				"/v1/relay/events",
				candidate.SentAt,
				candidate.Nonce,
				candidateBody,
			)
			if signErr != nil {
				t.Fatal(signErr)
			}
			if _, verifyErr := VerifyPeer(
				test.peer,
				test.scope,
				"POST",
				"/v1/relay/events",
				candidateBody,
				candidateSignature,
				test.now,
				5*time.Minute,
			); verifyErr == nil {
				t.Fatal("expected peer verification error")
			}
		})
	}
}

func TestPeerValidationIsDenyByDefault(t *testing.T) {
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	valid := Peer{
		ID:              "peer-one",
		TeamID:          "team-one",
		OriginID:        "origin-one",
		PublicKey:       publicKey,
		Scopes:          []string{ScopeIngest},
		AllowedSources:  []string{"codex"},
		AllowedRuntimes: []string{"wsl"},
	}
	tests := []struct {
		name   string
		mutate func(*Peer)
	}{
		{"id", func(value *Peer) { value.ID = "" }},
		{"team", func(value *Peer) { value.TeamID = "" }},
		{"origin", func(value *Peer) { value.OriginID = "" }},
		{"public key", func(value *Peer) { value.PublicKey = nil }},
		{"scopes", func(value *Peer) { value.Scopes = nil }},
		{"scope syntax", func(value *Peer) { value.Scopes = []string{"bad scope"} }},
		{"source empty", func(value *Peer) { value.AllowedSources = nil }},
		{"runtime empty", func(value *Peer) { value.AllowedRuntimes = nil }},
		{"source", func(value *Peer) { value.AllowedSources = []string{"unknown"} }},
		{"runtime", func(value *Peer) { value.AllowedRuntimes = []string{"unknown"} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("expected peer validation error")
			}
		})
	}
}
