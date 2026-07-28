package remote

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/liming0791/agentbell/core/internal/event"
)

const maxPairingResponseBytes = 64 * 1024

var (
	ErrInvalidPairingClient        = errors.New("invalid remote pairing client configuration")
	ErrInvalidPairingClientOptions = errors.New("invalid remote pairing client options")
	ErrInvalidPairingRequest       = errors.New("invalid remote pairing request")
	ErrPairingRequest              = errors.New("remote pairing request failed")
	ErrPairingResponse             = errors.New("remote pairing response was invalid")

	pairingClientCodePattern = regexp.MustCompile(
		`^AGBR-[0123456789ABCDEFGHJKMNPQRSTVWXYZ]{8}` +
			`(?:-[0123456789ABCDEFGHJKMNPQRSTVWXYZ]{8}){3}$`,
	)
	pairingClientIdentifierPattern = regexp.MustCompile(
		`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`,
	)
	pairingClientHostPattern = regexp.MustCompile(
		`^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?$`,
	)
	pairingClientSPKIPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type PairingClientConfig struct {
	Endpoint   string
	PinnedSPKI string
	SSHTunnel  bool
}

func (config PairingClientConfig) String() string {
	return fmt.Sprintf(
		"remote.PairingClientConfig{Endpoint:<redacted>, PinnedSPKI:<redacted>, SSHTunnel:%t}",
		config.SSHTunnel,
	)
}

func (config PairingClientConfig) GoString() string {
	return config.String()
}

type PairingClientOptions struct {
	RootCAs               *x509.CertPool
	Timeout               time.Duration
	DialTimeout           time.Duration
	TLSHandshakeTimeout   time.Duration
	ResponseHeaderTimeout time.Duration
}

type PairingRequest struct {
	Code      string
	PeerID    string
	OriginID  string
	PublicKey ed25519.PublicKey
}

func (request PairingRequest) String() string {
	return fmt.Sprintf(
		"remote.PairingRequest{Code:<redacted>, PeerID:<redacted>, OriginID:<redacted>, PublicKey:<redacted:%d>}",
		len(request.PublicKey),
	)
}

func (request PairingRequest) GoString() string {
	return request.String()
}

type PairingResult struct {
	PeerID          string
	TeamID          string
	AllowedSources  []string
	AllowedRuntimes []string
}

type PairingClient struct {
	endpoint *url.URL
	client   *http.Client
}

func NewPairingClient(
	config PairingClientConfig,
	options PairingClientOptions,
) (*PairingClient, error) {
	endpoint, err := validatePairingEndpoint(config)
	if err != nil {
		return nil, ErrInvalidPairingClient
	}
	httpClient, err := newSecureHTTPClient(HTTPSOptions{
		RootCAs:               options.RootCAs,
		Timeout:               options.Timeout,
		DialTimeout:           options.DialTimeout,
		TLSHandshakeTimeout:   options.TLSHandshakeTimeout,
		ResponseHeaderTimeout: options.ResponseHeaderTimeout,
	}, config.PinnedSPKI)
	if err != nil {
		return nil, ErrInvalidPairingClientOptions
	}
	return &PairingClient{
		endpoint: endpoint,
		client:   httpClient,
	}, nil
}

func (client *PairingClient) String() string {
	return "remote.PairingClient{Endpoint:<redacted>}"
}

func (client *PairingClient) GoString() string {
	return client.String()
}

func (client *PairingClient) Pair(
	ctx context.Context,
	request PairingRequest,
) (PairingResult, error) {
	if ctx == nil {
		return PairingResult{}, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return PairingResult{}, err
	}
	if client == nil || client.endpoint == nil || client.client == nil {
		return PairingResult{}, ErrPairingRequest
	}
	canonicalCode, err := validatePairingRequest(request)
	if err != nil {
		return PairingResult{}, ErrInvalidPairingRequest
	}
	encoded, err := json.Marshal(struct {
		Code      string `json:"code"`
		PeerID    string `json:"peerId"`
		OriginID  string `json:"originId"`
		PublicKey string `json:"publicKey"`
	}{
		Code:      canonicalCode,
		PeerID:    request.PeerID,
		OriginID:  request.OriginID,
		PublicKey: base64.RawURLEncoding.EncodeToString(request.PublicKey),
	})
	if err != nil ||
		len(encoded) == 0 ||
		len(encoded) > maxPairingResponseBytes {
		return PairingResult{}, ErrInvalidPairingRequest
	}
	httpRequest, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		client.endpoint.String(),
		bytes.NewReader(encoded),
	)
	if err != nil {
		return PairingResult{}, ErrPairingRequest
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("Cache-Control", "no-store")
	response, err := client.client.Do(httpRequest)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return PairingResult{}, ctxErr
		}
		return PairingResult{}, ErrPairingRequest
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		return PairingResult{}, ErrPairingResponse
	}
	mediaType, _, err := mime.ParseMediaType(
		response.Header.Get("Content-Type"),
	)
	if err != nil || mediaType != "application/json" {
		return PairingResult{}, ErrPairingResponse
	}
	body, err := io.ReadAll(
		io.LimitReader(response.Body, maxPairingResponseBytes+1),
	)
	if err != nil || len(body) == 0 || len(body) > maxPairingResponseBytes {
		return PairingResult{}, ErrPairingResponse
	}
	result, err := decodePairingResult(body)
	if err != nil || result.PeerID != request.PeerID {
		return PairingResult{}, ErrPairingResponse
	}
	if err := result.validate(); err != nil {
		return PairingResult{}, ErrPairingResponse
	}
	return result, nil
}

func (client *PairingClient) Close() error {
	if client == nil || client.client == nil {
		return nil
	}
	if closer, ok := client.client.Transport.(interface {
		CloseIdleConnections()
	}); ok {
		closer.CloseIdleConnections()
	}
	return nil
}

func validatePairingEndpoint(
	config PairingClientConfig,
) (*url.URL, error) {
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil ||
		endpoint == nil ||
		endpoint.Opaque != "" ||
		endpoint.Host == "" ||
		endpoint.User != nil ||
		endpoint.Fragment != "" ||
		endpoint.RawQuery != "" ||
		endpoint.Path != "/v1/pair" ||
		endpoint.EscapedPath() != "/v1/pair" {
		return nil, ErrInvalidPairingClient
	}
	host := endpoint.Hostname()
	if !validPairingHost(host) {
		return nil, ErrInvalidPairingClient
	}
	if port := endpoint.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return nil, ErrInvalidPairingClient
		}
	}
	switch endpoint.Scheme {
	case "https":
	case "http":
		if !config.SSHTunnel || !isPairingLoopback(host) ||
			config.PinnedSPKI != "" {
			return nil, ErrInvalidPairingClient
		}
	default:
		return nil, ErrInvalidPairingClient
	}
	if config.PinnedSPKI != "" &&
		!pairingClientSPKIPattern.MatchString(config.PinnedSPKI) {
		return nil, ErrInvalidPairingClient
	}
	return endpoint, nil
}

func validatePairingRequest(request PairingRequest) (string, error) {
	code := strings.ToUpper(strings.TrimSpace(request.Code))
	if !pairingClientCodePattern.MatchString(code) ||
		!pairingClientIdentifierPattern.MatchString(request.PeerID) ||
		!pairingClientIdentifierPattern.MatchString(request.OriginID) ||
		len(request.PublicKey) != ed25519.PublicKeySize {
		return "", ErrInvalidPairingRequest
	}
	return code, nil
}

func (result PairingResult) validate() error {
	if !pairingClientIdentifierPattern.MatchString(result.PeerID) ||
		!pairingClientIdentifierPattern.MatchString(result.TeamID) ||
		len(result.AllowedSources) == 0 ||
		len(result.AllowedRuntimes) == 0 {
		return ErrPairingResponse
	}
	sources := make(map[string]bool, len(result.AllowedSources))
	for _, source := range result.AllowedSources {
		if !event.IsKnownSource(source) || sources[source] {
			return ErrPairingResponse
		}
		sources[source] = true
	}
	runtimes := make(map[string]bool, len(result.AllowedRuntimes))
	for _, runtimeName := range result.AllowedRuntimes {
		if !event.IsKnownRuntime(runtimeName) ||
			runtimeName == "host" ||
			runtimes[runtimeName] {
			return ErrPairingResponse
		}
		runtimes[runtimeName] = true
	}
	return nil
}

func decodePairingResult(body []byte) (PairingResult, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	opening, ok := token.(json.Delim)
	if err != nil || !ok || opening != '{' {
		return PairingResult{}, ErrPairingResponse
	}
	var peerID *string
	var teamID *string
	var allowedSources *[]string
	var allowedRuntimes *[]string
	seen := make(map[string]bool, 4)
	for decoder.More() {
		keyToken, err := decoder.Token()
		key, ok := keyToken.(string)
		if err != nil || !ok || seen[key] {
			return PairingResult{}, ErrPairingResponse
		}
		seen[key] = true
		switch key {
		case "peerId":
			var value string
			if decoder.Decode(&value) != nil {
				return PairingResult{}, ErrPairingResponse
			}
			peerID = &value
		case "teamId":
			var value string
			if decoder.Decode(&value) != nil {
				return PairingResult{}, ErrPairingResponse
			}
			teamID = &value
		case "allowedSources":
			var value []string
			if decoder.Decode(&value) != nil || value == nil {
				return PairingResult{}, ErrPairingResponse
			}
			allowedSources = &value
		case "allowedRuntimes":
			var value []string
			if decoder.Decode(&value) != nil || value == nil {
				return PairingResult{}, ErrPairingResponse
			}
			allowedRuntimes = &value
		default:
			return PairingResult{}, ErrPairingResponse
		}
	}
	if _, err := decoder.Token(); err != nil ||
		peerID == nil ||
		teamID == nil ||
		allowedSources == nil ||
		allowedRuntimes == nil {
		return PairingResult{}, ErrPairingResponse
	}
	if token, err := decoder.Token(); err != io.EOF || token != nil {
		return PairingResult{}, ErrPairingResponse
	}
	return PairingResult{
		PeerID:          *peerID,
		TeamID:          *teamID,
		AllowedSources:  append([]string(nil), (*allowedSources)...),
		AllowedRuntimes: append([]string(nil), (*allowedRuntimes)...),
	}, nil
}

func validPairingHost(host string) bool {
	if host == "" || strings.ContainsAny(host, "\x00\r\n \t/@?#;|&") {
		return false
	}
	return net.ParseIP(host) != nil || pairingClientHostPattern.MatchString(host)
}

func isPairingLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}
