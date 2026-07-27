package remote

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/liming0791/agentbell/core/internal/relay"
)

func validPairHello() PairHello {
	return PairHello{
		ProtocolVersion: PairProtocolVersion,
		TeamID:          "team-main",
		OriginID:        "origin-main",
		Runtime:         "wsl",
		PeerID:          "peer-main",
		PublicKey:       bytes.Repeat([]byte{0x42}, ed25519.PublicKeySize),
	}
}

func validPairDecision() PairDecision {
	return PairDecision{
		Accepted:        true,
		PeerID:          "peer-main",
		TeamID:          "team-main",
		AllowedSources:  []string{"codex", "claude"},
		AllowedRuntimes: []string{"wsl", "ssh"},
	}
}

func TestPairProtocolRoundTripsMetadataFrames(t *testing.T) {
	var helloWire bytes.Buffer
	hello := validPairHello()
	if err := WritePairHello(&helloWire, hello); err != nil {
		t.Fatal(err)
	}
	frame, err := relay.ReadFrame(bytes.NewReader(helloWire.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if frame.Kind != relay.FrameKindMetadata {
		t.Fatalf("hello frame kind = %d", frame.Kind)
	}
	if !bytes.Contains(frame.Body, []byte(`"protocolVersion":1`)) ||
		!bytes.Contains(frame.Body, []byte(base64.RawURLEncoding.EncodeToString(hello.PublicKey))) {
		t.Fatalf("unexpected hello wire shape: %s", frame.Body)
	}
	gotHello, err := ReadPairHello(bytes.NewReader(helloWire.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if gotHello.ProtocolVersion != hello.ProtocolVersion ||
		gotHello.TeamID != hello.TeamID ||
		gotHello.OriginID != hello.OriginID ||
		gotHello.Runtime != hello.Runtime ||
		gotHello.PeerID != hello.PeerID ||
		!bytes.Equal(gotHello.PublicKey, hello.PublicKey) {
		t.Fatalf("hello differs: %#v", gotHello)
	}

	for _, decision := range []PairDecision{
		validPairDecision(),
		{Accepted: false, ErrorCode: PairErrorEnrollmentFailed},
	} {
		var wire bytes.Buffer
		if err := WritePairDecision(&wire, decision); err != nil {
			t.Fatal(err)
		}
		got, err := ReadPairDecision(bytes.NewReader(wire.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		if got.Accepted != decision.Accepted ||
			got.PeerID != decision.PeerID ||
			got.TeamID != decision.TeamID ||
			got.ErrorCode != decision.ErrorCode ||
			strings.Join(got.AllowedSources, ",") != strings.Join(decision.AllowedSources, ",") ||
			strings.Join(got.AllowedRuntimes, ",") != strings.Join(decision.AllowedRuntimes, ",") {
			t.Fatalf("decision differs: %#v", got)
		}
	}
}

func TestPairProtocolRejectsMalformedStrictJSON(t *testing.T) {
	key := base64.RawURLEncoding.EncodeToString(validPairHello().PublicKey)
	validHello := `{"protocolVersion":1,"teamId":"team-main","originId":"origin-main","runtime":"wsl","peerId":"peer-main","publicKey":"` + key + `"}`
	helloBodies := []string{
		``,
		`[]`,
		`{}`,
		strings.Replace(validHello, `"peerId":"peer-main"`, `"peerId":"peer-main","peerId":"peer-other"`, 1),
		strings.TrimSuffix(validHello, "}") + `,"privateKey":"secret"}`,
		validHello + `{}`,
		strings.Replace(validHello, `"protocolVersion":1`, `"protocolVersion":"1"`, 1),
		strings.Replace(validHello, `"teamId":"team-main"`, `"teamId":null`, 1),
		strings.Replace(validHello, `"runtime":"wsl"`, `"runtime":"host"`, 1),
		strings.Replace(validHello, key, key+"=", 1),
	}
	for _, body := range helloBodies {
		var wire bytes.Buffer
		if body != "" {
			if err := relay.WriteFrame(&wire, relay.Frame{
				Kind: relay.FrameKindMetadata,
				Body: []byte(body),
			}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := ReadPairHello(bytes.NewReader(wire.Bytes())); !errors.Is(err, ErrPairProtocol) {
			t.Fatalf("hello body %q error = %v", body, err)
		}
	}

	validAccepted := `{"accepted":true,"peerId":"peer-main","teamId":"team-main","allowedSources":["codex"],"allowedRuntimes":["wsl"]}`
	decisionBodies := []string{
		`{}`,
		`{"accepted":false}`,
		`{"accepted":false,"errorCode":"unknown"}`,
		`{"accepted":false,"errorCode":"invalid_hello","peerId":"peer-main"}`,
		strings.Replace(validAccepted, `"accepted":true`, `"accepted":true,"accepted":false`, 1),
		strings.TrimSuffix(validAccepted, "}") + `,"code":"bearer-secret"}`,
		validAccepted + `null`,
		`{"accepted":true,"peerId":"peer-main","teamId":"team-main","allowedSources":null,"allowedRuntimes":["wsl"]}`,
	}
	for _, body := range decisionBodies {
		var wire bytes.Buffer
		if err := relay.WriteFrame(&wire, relay.Frame{
			Kind: relay.FrameKindMetadata,
			Body: []byte(body),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadPairDecision(bytes.NewReader(wire.Bytes())); !errors.Is(err, ErrPairProtocol) {
			t.Fatalf("decision body %q error = %v", body, err)
		}
	}
}

func TestPairProtocolRejectsWrongKindOversizeAndInvalidWrites(t *testing.T) {
	var wrongKind bytes.Buffer
	if err := relay.WriteFrame(&wrongKind, relay.Frame{
		Kind: relay.FrameKindEnvelope,
		Body: []byte(`{}`),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadPairHello(&wrongKind); !errors.Is(err, ErrPairProtocol) {
		t.Fatalf("wrong kind error = %v", err)
	}

	header := make([]byte, relay.FrameHeaderBytes)
	copy(header, relay.FrameMagic)
	header[4] = relay.FrameVersion
	header[5] = byte(relay.FrameKindMetadata)
	header[8] = 0x00
	header[9] = 0x01
	header[10] = 0x00
	header[11] = 0x01
	if _, err := ReadPairHello(bytes.NewReader(header)); !errors.Is(err, ErrPairMessageTooLarge) {
		t.Fatalf("oversized error = %v", err)
	}
	if err := WritePairHello(nil, validPairHello()); !errors.Is(err, ErrPairProtocol) {
		t.Fatalf("nil writer error = %v", err)
	}
	if _, err := ReadPairDecision(nil); !errors.Is(err, ErrPairProtocol) {
		t.Fatalf("nil reader error = %v", err)
	}

	invalidHello := validPairHello()
	invalidHello.PublicKey = []byte("short")
	if err := WritePairHello(io.Discard, invalidHello); !errors.Is(err, ErrPairProtocol) {
		t.Fatalf("invalid hello write error = %v", err)
	}
	invalidDecision := validPairDecision()
	invalidDecision.ErrorCode = PairErrorInvalidHello
	if err := WritePairDecision(io.Discard, invalidDecision); !errors.Is(err, ErrPairProtocol) {
		t.Fatalf("invalid decision write error = %v", err)
	}
}

type leakingWriter struct {
	secret string
}

func (writer leakingWriter) Write([]byte) (int, error) {
	return 0, errors.New("writer exposed " + writer.secret)
}

func TestPairProtocolFormattingAndErrorsAreRedacted(t *testing.T) {
	secret := "AGBR-01234567-89ABCDEF-GHJKMNPQ-RSTVWXYZ"
	hello := validPairHello()
	hello.TeamID = secret
	decision := validPairDecision()
	decision.TeamID = secret
	for _, formatted := range []string{
		hello.String(),
		hello.GoString(),
		decision.String(),
		decision.GoString(),
	} {
		if strings.Contains(formatted, secret) ||
			strings.Contains(formatted, hello.PeerID) ||
			strings.Contains(formatted, decision.PeerID) {
			t.Fatalf("formatter leaked metadata: %q", formatted)
		}
	}
	err := WritePairDecision(
		leakingWriter{secret: secret},
		validPairDecision(),
	)
	if !errors.Is(err, ErrPairProtocol) || strings.Contains(err.Error(), secret) {
		t.Fatalf("writer error was not redacted: %v", err)
	}
}
