package relay

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
)

const pairingFailureMessage = "pairing failed"

// PairEnrollmentRequest is the validated, typed input passed to the app-owned
// enrollment transaction. String and GoString deliberately redact every field
// because Code is a bearer secret and identifiers and keys are private
// operational metadata.
type PairEnrollmentRequest struct {
	Code      string
	PeerID    string
	OriginID  string
	PublicKey ed25519.PublicKey
}

func (request PairEnrollmentRequest) String() string {
	return fmt.Sprintf(
		"relay.PairEnrollmentRequest{Code:<redacted>, PeerID:<redacted>, OriginID:<redacted>, PublicKey:<redacted:%d>}",
		len(request.PublicKey),
	)
}

func (request PairEnrollmentRequest) GoString() string {
	return request.String()
}

// PairEnrollmentResult contains only the non-secret policy fields that the
// pairing endpoint is allowed to return.
type PairEnrollmentResult struct {
	PeerID          string
	TeamID          string
	AllowedSources  []string
	AllowedRuntimes []string
}

// PairEnrollmentFunc owns the state transaction outside the HTTP layer. An app
// implementation claims PairingStore, atomically adds the relay peer, commits
// the pairing claim, and compensates or releases on any failure.
type PairEnrollmentFunc func(
	context.Context,
	PairEnrollmentRequest,
) (PairEnrollmentResult, error)

type pairingHTTPHandler struct {
	enroll PairEnrollmentFunc
}

func NewPairingHTTPHandler(enroll PairEnrollmentFunc) http.Handler {
	return pairingHTTPHandler{enroll: enroll}
}

func (handler pairingHTTPHandler) ServeHTTP(
	response http.ResponseWriter,
	request *http.Request,
) {
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", http.MethodPost)
		writePairingFailure(response, http.StatusMethodNotAllowed)
		return
	}
	if request.URL == nil ||
		request.URL.Path != "/v1/pair" ||
		request.URL.EscapedPath() != "/v1/pair" ||
		request.URL.RawQuery != "" {
		writePairingFailure(response, http.StatusNotFound)
		return
	}
	mediaType, _, err := mime.ParseMediaType(
		request.Header.Get("Content-Type"),
	)
	if err != nil || mediaType != "application/json" {
		writePairingFailure(response, http.StatusUnsupportedMediaType)
		return
	}
	if request.Body == nil {
		writePairingFailure(response, http.StatusBadRequest)
		return
	}
	if request.ContentLength > MaxBodyBytes {
		writePairingFailure(response, http.StatusRequestEntityTooLarge)
		return
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, MaxBodyBytes+1))
	if err != nil {
		writePairingFailure(response, http.StatusBadRequest)
		return
	}
	if len(body) == 0 {
		writePairingFailure(response, http.StatusBadRequest)
		return
	}
	if len(body) > MaxBodyBytes {
		writePairingFailure(response, http.StatusRequestEntityTooLarge)
		return
	}
	enrollmentRequest, err := decodePairEnrollmentRequest(body)
	if err != nil {
		writePairingFailure(response, http.StatusBadRequest)
		return
	}
	if handler.enroll == nil {
		writePairingFailure(response, http.StatusServiceUnavailable)
		return
	}
	result, err, panicked := callPairEnrollment(
		handler.enroll,
		request.Context(),
		enrollmentRequest,
	)
	if panicked {
		writePairingFailure(response, http.StatusInternalServerError)
		return
	}
	if err != nil {
		writePairingFailure(response, http.StatusUnauthorized)
		return
	}
	result.AllowedSources = append([]string(nil), result.AllowedSources...)
	result.AllowedRuntimes = append([]string(nil), result.AllowedRuntimes...)
	if err := result.validate(enrollmentRequest); err != nil {
		writePairingFailure(response, http.StatusInternalServerError)
		return
	}
	encoded, err := json.Marshal(struct {
		PeerID          string   `json:"peerId"`
		TeamID          string   `json:"teamId"`
		AllowedSources  []string `json:"allowedSources"`
		AllowedRuntimes []string `json:"allowedRuntimes"`
	}{
		PeerID:          result.PeerID,
		TeamID:          result.TeamID,
		AllowedSources:  result.AllowedSources,
		AllowedRuntimes: result.AllowedRuntimes,
	})
	if err != nil {
		writePairingFailure(response, http.StatusInternalServerError)
		return
	}
	response.WriteHeader(http.StatusCreated)
	_, _ = response.Write(append(encoded, '\n'))
}

type pairEnrollmentWireRequest struct {
	Code      string `json:"code"`
	PeerID    string `json:"peerId"`
	OriginID  string `json:"originId"`
	PublicKey string `json:"publicKey"`
}

func decodePairEnrollmentRequest(
	body []byte,
) (PairEnrollmentRequest, error) {
	var wire pairEnrollmentWireRequest
	if err := strictWireJSON(body, &wire); err != nil {
		return PairEnrollmentRequest{}, errors.New("invalid pairing request")
	}
	if _, _, err := pairingIdentity(wire.Code); err != nil {
		return PairEnrollmentRequest{}, errors.New("invalid pairing request")
	}
	if err := validateIdentifier("peer id", wire.PeerID); err != nil {
		return PairEnrollmentRequest{}, errors.New("invalid pairing request")
	}
	if err := validateIdentifier("origin id", wire.OriginID); err != nil {
		return PairEnrollmentRequest{}, errors.New("invalid pairing request")
	}
	publicKey, err := base64.RawURLEncoding.DecodeString(wire.PublicKey)
	if err != nil ||
		len(publicKey) != ed25519.PublicKeySize ||
		base64.RawURLEncoding.EncodeToString(publicKey) != wire.PublicKey {
		return PairEnrollmentRequest{}, errors.New("invalid pairing request")
	}
	return PairEnrollmentRequest{
		Code:      wire.Code,
		PeerID:    wire.PeerID,
		OriginID:  wire.OriginID,
		PublicKey: append(ed25519.PublicKey(nil), publicKey...),
	}, nil
}

func (result PairEnrollmentResult) validate(
	request PairEnrollmentRequest,
) error {
	if result.PeerID != request.PeerID {
		return errors.New("pairing result peer does not match request")
	}
	return (PairingPolicy{
		TeamID:          result.TeamID,
		AllowedSources:  result.AllowedSources,
		AllowedRuntimes: result.AllowedRuntimes,
	}).Validate()
}

func callPairEnrollment(
	enroll PairEnrollmentFunc,
	ctx context.Context,
	request PairEnrollmentRequest,
) (
	result PairEnrollmentResult,
	err error,
	panicked bool,
) {
	defer func() {
		if recover() != nil {
			result = PairEnrollmentResult{}
			err = nil
			panicked = true
		}
	}()
	result, err = enroll(ctx, request)
	return result, err, false
}

func writePairingFailure(
	response http.ResponseWriter,
	status int,
) {
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(
		map[string]string{"error": pairingFailureMessage},
	)
}
