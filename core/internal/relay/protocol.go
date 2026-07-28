package relay

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/liming0791/agentbell/core/internal/event"
)

const (
	ProtocolVersion = "1"
	MaxBodyBytes    = 64 * 1024
	MaxHop          = 4
	ScopeIngest     = "relay:ingest"
	DefaultMaxSkew  = 5 * time.Minute
)

var (
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	noncePattern      = regexp.MustCompile(`^[0-9a-f]{32}$`)
	deliveryPattern   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type Origin struct {
	ID      string `json:"id"`
	Runtime string `json:"runtime"`
}

type Delivery struct {
	Key         string `json:"key"`
	ProducerKey string `json:"producerKey"`
}

type Envelope struct {
	ProtocolVersion string             `json:"protocolVersion"`
	TeamID          string             `json:"teamId"`
	Origin          Origin             `json:"origin"`
	Delivery        Delivery           `json:"delivery"`
	SentAt          time.Time          `json:"sentAt"`
	Nonce           string             `json:"nonce"`
	Hop             int                `json:"hop"`
	Event           event.Notification `json:"event"`
}

type Peer struct {
	ID              string
	TeamID          string
	OriginID        string
	PublicKey       ed25519.PublicKey
	Scopes          []string
	AllowedSources  []string
	AllowedRuntimes []string
	Revoked         bool
}

func Decode(body []byte) (Envelope, error) {
	if len(body) == 0 {
		return Envelope{}, errors.New("relay envelope is empty")
	}
	if len(body) > MaxBodyBytes {
		return Envelope{}, fmt.Errorf(
			"relay envelope exceeds %d bytes",
			MaxBodyBytes,
		)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var envelope Envelope
	if err := decoder.Decode(&envelope); err != nil {
		return Envelope{}, fmt.Errorf("decode relay envelope: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return Envelope{}, errors.New("relay envelope contains trailing JSON")
		}
		return Envelope{}, fmt.Errorf("decode relay envelope trailing data: %w", err)
	}
	if err := envelope.Validate(); err != nil {
		return Envelope{}, err
	}
	return envelope, nil
}

func (envelope Envelope) Validate() error {
	if envelope.ProtocolVersion != ProtocolVersion {
		return fmt.Errorf(
			"unsupported relay protocol version %q",
			envelope.ProtocolVersion,
		)
	}
	if err := validateIdentifier("teamId", envelope.TeamID); err != nil {
		return err
	}
	if err := validateIdentifier("origin.id", envelope.Origin.ID); err != nil {
		return err
	}
	if !event.IsKnownRuntime(envelope.Origin.Runtime) {
		return fmt.Errorf("unsupported origin runtime %q", envelope.Origin.Runtime)
	}
	if envelope.Origin.Runtime != envelope.Event.Runtime {
		return errors.New("origin runtime does not match event runtime")
	}
	if envelope.Delivery.ProducerKey == "" ||
		len(envelope.Delivery.ProducerKey) > 512 {
		return errors.New("delivery producerKey must contain 1 to 512 bytes")
	}
	expected, err := DeriveDeliveryKey(
		envelope.TeamID,
		envelope.Origin.ID,
		envelope.Delivery.ProducerKey,
	)
	if err != nil {
		return err
	}
	if !deliveryPattern.MatchString(envelope.Delivery.Key) ||
		envelope.Delivery.Key != expected {
		return errors.New("delivery key does not match team, origin and producer key")
	}
	if envelope.Delivery.ProducerKey != envelope.Event.IdempotencyKey {
		return errors.New("delivery producerKey does not match event idempotencyKey")
	}
	if envelope.SentAt.IsZero() {
		return errors.New("sentAt is required")
	}
	if err := validateNonce(envelope.Nonce); err != nil {
		return err
	}
	if envelope.Hop < 0 || envelope.Hop > MaxHop {
		return fmt.Errorf("hop must be between 0 and %d", MaxHop)
	}
	if err := envelope.Event.Validate(); err != nil {
		return fmt.Errorf("invalid relay event: %w", err)
	}
	return nil
}

func DeriveDeliveryKey(teamID, originID, producerKey string) (string, error) {
	if err := validateIdentifier("teamId", teamID); err != nil {
		return "", err
	}
	if err := validateIdentifier("origin.id", originID); err != nil {
		return "", err
	}
	if producerKey == "" || len(producerKey) > 512 {
		return "", errors.New("producer key must contain 1 to 512 bytes")
	}

	hash := sha256.New()
	_, _ = hash.Write([]byte("agentbell-relay-delivery-v1"))
	writeHashField(hash, teamID)
	writeHashField(hash, originID)
	writeHashField(hash, producerKey)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func SigningMaterial(
	method string,
	target string,
	sentAt time.Time,
	nonce string,
	exactBody []byte,
) ([]byte, error) {
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" || strings.ContainsAny(method, "\x00\r\n") {
		return nil, errors.New("signature method is invalid")
	}
	if target == "" || len(target) > 2048 || strings.ContainsAny(target, "\x00\r\n") {
		return nil, errors.New("signature target is invalid")
	}
	if sentAt.IsZero() {
		return nil, errors.New("signature sentAt is required")
	}
	if err := validateNonce(nonce); err != nil {
		return nil, err
	}
	if len(exactBody) == 0 || len(exactBody) > MaxBodyBytes {
		return nil, fmt.Errorf(
			"signature body must contain 1 to %d bytes",
			MaxBodyBytes,
		)
	}
	bodyHash := sha256.Sum256(exactBody)
	material := strings.Join([]string{
		"AGENTBELL-RELAY-SIGNATURE-V1",
		method,
		target,
		sentAt.UTC().Format(time.RFC3339Nano),
		nonce,
		hex.EncodeToString(bodyHash[:]),
	}, "\n")
	return []byte(material), nil
}

func Sign(
	privateKey ed25519.PrivateKey,
	method string,
	target string,
	sentAt time.Time,
	nonce string,
	exactBody []byte,
) ([]byte, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid Ed25519 private key")
	}
	material, err := SigningMaterial(method, target, sentAt, nonce, exactBody)
	if err != nil {
		return nil, err
	}
	return ed25519.Sign(privateKey, material), nil
}

func Verify(
	publicKey ed25519.PublicKey,
	method string,
	target string,
	sentAt time.Time,
	nonce string,
	exactBody []byte,
	signature []byte,
) error {
	if len(publicKey) != ed25519.PublicKeySize {
		return errors.New("invalid Ed25519 public key")
	}
	if len(signature) != ed25519.SignatureSize {
		return errors.New("invalid Ed25519 signature")
	}
	material, err := SigningMaterial(method, target, sentAt, nonce, exactBody)
	if err != nil {
		return err
	}
	if !ed25519.Verify(publicKey, material, signature) {
		return errors.New("relay signature verification failed")
	}
	return nil
}

func VerifyPeer(
	peer Peer,
	requiredScope string,
	method string,
	target string,
	exactBody []byte,
	signature []byte,
	now time.Time,
	maxSkew time.Duration,
) (Envelope, error) {
	envelope, err := Decode(exactBody)
	if err != nil {
		return Envelope{}, err
	}
	if err := peer.Validate(); err != nil {
		return Envelope{}, err
	}
	if peer.Revoked {
		return Envelope{}, errors.New("relay peer is revoked")
	}
	if requiredScope == "" || !contains(peer.Scopes, requiredScope) {
		return Envelope{}, fmt.Errorf("relay peer lacks scope %q", requiredScope)
	}
	if peer.TeamID != envelope.TeamID {
		return Envelope{}, errors.New("relay peer team does not match envelope")
	}
	if peer.OriginID != envelope.Origin.ID {
		return Envelope{}, errors.New("relay peer origin does not match envelope")
	}
	if !contains(peer.AllowedSources, envelope.Event.Source) {
		return Envelope{}, fmt.Errorf(
			"relay peer cannot submit source %q",
			envelope.Event.Source,
		)
	}
	if !contains(peer.AllowedRuntimes, envelope.Event.Runtime) {
		return Envelope{}, fmt.Errorf(
			"relay peer cannot submit runtime %q",
			envelope.Event.Runtime,
		)
	}
	if now.IsZero() {
		return Envelope{}, errors.New("verification time is required")
	}
	if maxSkew <= 0 {
		maxSkew = DefaultMaxSkew
	}
	if envelope.SentAt.Before(now.UTC().Add(-maxSkew)) {
		return Envelope{}, errors.New("relay envelope timestamp is expired")
	}
	if envelope.SentAt.After(now.UTC().Add(maxSkew)) {
		return Envelope{}, errors.New("relay envelope timestamp is too far in the future")
	}
	if err := Verify(
		peer.PublicKey,
		method,
		target,
		envelope.SentAt,
		envelope.Nonce,
		exactBody,
		signature,
	); err != nil {
		return Envelope{}, err
	}
	return envelope, nil
}

func (peer Peer) Validate() error {
	if err := validateIdentifier("peer id", peer.ID); err != nil {
		return err
	}
	if err := validateIdentifier("peer teamId", peer.TeamID); err != nil {
		return err
	}
	if err := validateIdentifier("peer originId", peer.OriginID); err != nil {
		return err
	}
	if len(peer.PublicKey) != ed25519.PublicKeySize {
		return errors.New("relay peer has invalid Ed25519 public key")
	}
	if len(peer.Scopes) == 0 {
		return errors.New("relay peer requires at least one scope")
	}
	for _, scope := range peer.Scopes {
		if err := validateIdentifier("peer scope", scope); err != nil {
			return err
		}
	}
	if len(peer.AllowedSources) == 0 || len(peer.AllowedRuntimes) == 0 {
		return errors.New("relay peer source and runtime allowlists cannot be empty")
	}
	for _, source := range peer.AllowedSources {
		if !event.IsKnownSource(source) {
			return fmt.Errorf("relay peer has unsupported source %q", source)
		}
	}
	for _, runtimeName := range peer.AllowedRuntimes {
		if !event.IsKnownRuntime(runtimeName) {
			return fmt.Errorf("relay peer has unsupported runtime %q", runtimeName)
		}
	}
	return nil
}

func validateIdentifier(name, value string) error {
	if !identifierPattern.MatchString(value) {
		return fmt.Errorf("%s is invalid", name)
	}
	return nil
}

func validateNonce(value string) error {
	if !noncePattern.MatchString(value) {
		return errors.New("nonce must be 16 bytes encoded as lowercase hexadecimal")
	}
	return nil
}

func writeHashField(hash io.Writer, value string) {
	_ = binary.Write(hash, binary.BigEndian, uint32(len(value)))
	_, _ = io.WriteString(hash, value)
}

func contains(values []string, candidate string) bool {
	for _, value := range values {
		if value == candidate {
			return true
		}
	}
	return false
}
