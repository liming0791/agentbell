package remote

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"unicode/utf8"

	"github.com/liming0791/agentbell/core/internal/event"
	"github.com/liming0791/agentbell/core/internal/relay"
)

const PairProtocolVersion = 1

const (
	PairErrorInvalidHello     = "invalid_hello"
	PairErrorEnrollmentFailed = "enrollment_failed"
)

var (
	ErrPairProtocol        = errors.New("remote stdio pairing protocol failed")
	ErrPairMessageTooLarge = errors.New("remote stdio pairing message exceeds 64 KiB")
)

var pairIdentifierPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// PairHello is emitted by the remote child before the host consumes a pairing
// code. Formatting redacts every field because identifiers and public keys are
// private operational metadata.
type PairHello struct {
	ProtocolVersion int
	TeamID          string
	OriginID        string
	Runtime         string
	PeerID          string
	PublicKey       ed25519.PublicKey
}

func (hello PairHello) String() string {
	return "remote.PairHello{<redacted>}"
}

func (hello PairHello) GoString() string {
	return hello.String()
}

// PairDecision is the host's final policy decision. A rejection contains only
// one stable ErrorCode; no enrollment error or bearer code crosses stdio.
type PairDecision struct {
	Accepted        bool
	PeerID          string
	TeamID          string
	AllowedSources  []string
	AllowedRuntimes []string
	ErrorCode       string
}

func (decision PairDecision) String() string {
	return "remote.PairDecision{<redacted>}"
}

func (decision PairDecision) GoString() string {
	return decision.String()
}

func WritePairHello(writer io.Writer, hello PairHello) error {
	if writer == nil || hello.validate() != nil {
		return ErrPairProtocol
	}
	body, err := json.Marshal(struct {
		ProtocolVersion int    `json:"protocolVersion"`
		TeamID          string `json:"teamId"`
		OriginID        string `json:"originId"`
		Runtime         string `json:"runtime"`
		PeerID          string `json:"peerId"`
		PublicKey       string `json:"publicKey"`
	}{
		ProtocolVersion: hello.ProtocolVersion,
		TeamID:          hello.TeamID,
		OriginID:        hello.OriginID,
		Runtime:         hello.Runtime,
		PeerID:          hello.PeerID,
		PublicKey: base64.RawURLEncoding.EncodeToString(
			hello.PublicKey,
		),
	})
	if err != nil {
		return ErrPairProtocol
	}
	return writePairMetadata(writer, body)
}

func ReadPairHello(reader io.Reader) (PairHello, error) {
	body, err := readPairMetadata(reader)
	if err != nil {
		return PairHello{}, err
	}
	fields, err := decodePairObject(body, map[string]bool{
		"protocolVersion": true,
		"teamId":          true,
		"originId":        true,
		"runtime":         true,
		"peerId":          true,
		"publicKey":       true,
	})
	if err != nil || len(fields) != 6 {
		return PairHello{}, ErrPairProtocol
	}
	version, versionOK := decodePairInt(fields["protocolVersion"])
	teamID, teamOK := decodePairString(fields["teamId"])
	originID, originOK := decodePairString(fields["originId"])
	runtimeName, runtimeOK := decodePairString(fields["runtime"])
	peerID, peerOK := decodePairString(fields["peerId"])
	encodedKey, keyOK := decodePairString(fields["publicKey"])
	if !versionOK || !teamOK || !originOK || !runtimeOK || !peerOK || !keyOK {
		return PairHello{}, ErrPairProtocol
	}
	publicKey, err := base64.RawURLEncoding.Strict().DecodeString(encodedKey)
	if err != nil ||
		len(publicKey) != ed25519.PublicKeySize ||
		base64.RawURLEncoding.EncodeToString(publicKey) != encodedKey {
		return PairHello{}, ErrPairProtocol
	}
	hello := PairHello{
		ProtocolVersion: version,
		TeamID:          teamID,
		OriginID:        originID,
		Runtime:         runtimeName,
		PeerID:          peerID,
		PublicKey:       append(ed25519.PublicKey(nil), publicKey...),
	}
	if err := hello.validate(); err != nil {
		return PairHello{}, ErrPairProtocol
	}
	return hello, nil
}

func WritePairDecision(writer io.Writer, decision PairDecision) error {
	if writer == nil || decision.validate() != nil {
		return ErrPairProtocol
	}
	var body []byte
	var err error
	if decision.Accepted {
		body, err = json.Marshal(struct {
			Accepted        bool     `json:"accepted"`
			PeerID          string   `json:"peerId"`
			TeamID          string   `json:"teamId"`
			AllowedSources  []string `json:"allowedSources"`
			AllowedRuntimes []string `json:"allowedRuntimes"`
		}{
			Accepted:        true,
			PeerID:          decision.PeerID,
			TeamID:          decision.TeamID,
			AllowedSources:  decision.AllowedSources,
			AllowedRuntimes: decision.AllowedRuntimes,
		})
	} else {
		body, err = json.Marshal(struct {
			Accepted  bool   `json:"accepted"`
			ErrorCode string `json:"errorCode"`
		}{
			Accepted:  false,
			ErrorCode: decision.ErrorCode,
		})
	}
	if err != nil {
		return ErrPairProtocol
	}
	return writePairMetadata(writer, body)
}

func ReadPairDecision(reader io.Reader) (PairDecision, error) {
	body, err := readPairMetadata(reader)
	if err != nil {
		return PairDecision{}, err
	}
	fields, err := decodePairObject(body, map[string]bool{
		"accepted":        true,
		"peerId":          true,
		"teamId":          true,
		"allowedSources":  true,
		"allowedRuntimes": true,
		"errorCode":       true,
	})
	if err != nil {
		return PairDecision{}, ErrPairProtocol
	}
	accepted, ok := decodePairBool(fields["accepted"])
	if !ok {
		return PairDecision{}, ErrPairProtocol
	}
	if !accepted {
		if len(fields) != 2 {
			return PairDecision{}, ErrPairProtocol
		}
		errorCode, ok := decodePairString(fields["errorCode"])
		if !ok {
			return PairDecision{}, ErrPairProtocol
		}
		decision := PairDecision{ErrorCode: errorCode}
		if err := decision.validate(); err != nil {
			return PairDecision{}, ErrPairProtocol
		}
		return decision, nil
	}
	if len(fields) != 5 {
		return PairDecision{}, ErrPairProtocol
	}
	peerID, peerOK := decodePairString(fields["peerId"])
	teamID, teamOK := decodePairString(fields["teamId"])
	sources, sourcesOK := decodePairStrings(fields["allowedSources"])
	runtimes, runtimesOK := decodePairStrings(fields["allowedRuntimes"])
	if !peerOK || !teamOK || !sourcesOK || !runtimesOK {
		return PairDecision{}, ErrPairProtocol
	}
	decision := PairDecision{
		Accepted:        true,
		PeerID:          peerID,
		TeamID:          teamID,
		AllowedSources:  sources,
		AllowedRuntimes: runtimes,
	}
	if err := decision.validate(); err != nil {
		return PairDecision{}, ErrPairProtocol
	}
	return decision, nil
}

func (hello PairHello) validate() error {
	if hello.ProtocolVersion != PairProtocolVersion ||
		!pairIdentifierPattern.MatchString(hello.TeamID) ||
		!pairIdentifierPattern.MatchString(hello.OriginID) ||
		!pairIdentifierPattern.MatchString(hello.PeerID) ||
		!event.IsKnownRuntime(hello.Runtime) ||
		hello.Runtime == "host" ||
		len(hello.PublicKey) != ed25519.PublicKeySize {
		return ErrPairProtocol
	}
	return nil
}

func (decision PairDecision) validate() error {
	if !decision.Accepted {
		if decision.PeerID != "" ||
			decision.TeamID != "" ||
			decision.AllowedSources != nil ||
			decision.AllowedRuntimes != nil ||
			(decision.ErrorCode != PairErrorInvalidHello &&
				decision.ErrorCode != PairErrorEnrollmentFailed) {
			return ErrPairProtocol
		}
		return nil
	}
	if decision.ErrorCode != "" ||
		!pairIdentifierPattern.MatchString(decision.PeerID) {
		return ErrPairProtocol
	}
	return (relay.PairingPolicy{
		TeamID:          decision.TeamID,
		AllowedSources:  decision.AllowedSources,
		AllowedRuntimes: decision.AllowedRuntimes,
	}).Validate()
}

func writePairMetadata(writer io.Writer, body []byte) error {
	err := relay.WriteFrame(writer, relay.Frame{
		Kind: relay.FrameKindMetadata,
		Body: body,
	})
	if errors.Is(err, relay.ErrFrameTooLarge) {
		return ErrPairMessageTooLarge
	}
	if err != nil {
		return ErrPairProtocol
	}
	return nil
}

func readPairMetadata(reader io.Reader) ([]byte, error) {
	if reader == nil {
		return nil, ErrPairProtocol
	}
	frame, err := relay.ReadFrame(reader)
	if errors.Is(err, relay.ErrFrameTooLarge) {
		return nil, ErrPairMessageTooLarge
	}
	if err != nil || frame.Kind != relay.FrameKindMetadata {
		return nil, ErrPairProtocol
	}
	return frame.Body, nil
}

func decodePairObject(
	body []byte,
	allowed map[string]bool,
) (map[string]json.RawMessage, error) {
	if len(body) == 0 ||
		len(body) > relay.MaxFrameBodyBytes ||
		!utf8.Valid(body) {
		return nil, ErrPairProtocol
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, ErrPairProtocol
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, ErrPairProtocol
		}
		name, ok := token.(string)
		if !ok || !allowed[name] {
			return nil, ErrPairProtocol
		}
		if _, exists := fields[name]; exists {
			return nil, ErrPairProtocol
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, ErrPairProtocol
		}
		fields[name] = raw
	}
	token, err = decoder.Token()
	if err != nil || token != json.Delim('}') {
		return nil, ErrPairProtocol
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, ErrPairProtocol
	}
	return fields, nil
}

func decodePairString(raw json.RawMessage) (string, bool) {
	if raw == nil {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

func decodePairStrings(raw json.RawMessage) ([]string, bool) {
	if raw == nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, false
	}
	var value []string
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return nil, false
	}
	return append([]string(nil), value...), true
}

func decodePairInt(raw json.RawMessage) (int, bool) {
	if raw == nil {
		return 0, false
	}
	var value int
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, false
	}
	return value, true
}

func decodePairBool(raw json.RawMessage) (bool, bool) {
	if raw == nil {
		return false, false
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, false
	}
	return value, true
}

var _ fmt.Stringer = PairHello{}
var _ fmt.GoStringer = PairHello{}
var _ fmt.Stringer = PairDecision{}
var _ fmt.GoStringer = PairDecision{}
