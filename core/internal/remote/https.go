package remote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/liming0791/agentbell/core/internal/relay"
	"github.com/liming0791/agentbell/core/internal/remoteconfig"
)

const (
	maxHTTPSResponseBytes        = 64 * 1024
	defaultHTTPSTimeout          = 30 * time.Second
	defaultHTTPSDialTimeout      = 10 * time.Second
	defaultTLSHandshakeTimeout   = 10 * time.Second
	defaultResponseHeaderTimeout = 15 * time.Second
)

var (
	ErrNotHTTPSConnector   = errors.New("remote connector is not HTTPS")
	ErrInvalidHTTPSOptions = errors.New("invalid remote HTTPS connector options")
	ErrHTTPSRequest        = errors.New("remote HTTPS connector request failed")
	ErrHTTPSResponse       = errors.New("remote HTTPS connector response was invalid")
)

type HTTPSOptions struct {
	RootCAs               *x509.CertPool
	Now                   func() time.Time
	Timeout               time.Duration
	DialTimeout           time.Duration
	TLSHandshakeTimeout   time.Duration
	ResponseHeaderTimeout time.Duration
}

type HTTPSTransport struct {
	endpoint *url.URL
	client   *http.Client
	now      func() time.Time
}

func NewHTTPSTransport(
	config remoteconfig.RemoteConfig,
	options HTTPSOptions,
) (*HTTPSTransport, error) {
	if err := config.Validate(); err != nil {
		if errors.Is(err, remoteconfig.ErrVendorCloudUnsupported) {
			return nil, ErrVendorCloudUnsupported
		}
		return nil, ErrInvalidRemoteConfig
	}
	if config.Connector.Type != "https" || config.Connector.HTTPS == nil {
		return nil, ErrNotHTTPSConnector
	}
	if options.Timeout < 0 ||
		options.DialTimeout < 0 ||
		options.TLSHandshakeTimeout < 0 ||
		options.ResponseHeaderTimeout < 0 {
		return nil, ErrInvalidHTTPSOptions
	}
	endpoint, err := url.Parse(config.Connector.HTTPS.Endpoint)
	if err != nil ||
		endpoint.Scheme != "https" ||
		endpoint.RawQuery != "" ||
		endpoint.Path != "/v1/events" {
		return nil, ErrInvalidRemoteConfig
	}
	client, err := newSecureHTTPClient(
		options,
		config.Connector.HTTPS.PinnedSPKI,
	)
	if err != nil {
		return nil, err
	}
	return &HTTPSTransport{
		endpoint: endpoint,
		client:   client,
		now:      options.Now,
	}, nil
}

func newSecureHTTPClient(
	options HTTPSOptions,
	pin string,
) (*http.Client, error) {
	if options.Timeout < 0 ||
		options.DialTimeout < 0 ||
		options.TLSHandshakeTimeout < 0 ||
		options.ResponseHeaderTimeout < 0 {
		return nil, ErrInvalidHTTPSOptions
	}
	timeout := defaultDuration(options.Timeout, defaultHTTPSTimeout)
	dialTimeout := defaultDuration(
		options.DialTimeout,
		defaultHTTPSDialTimeout,
	)
	handshakeTimeout := defaultDuration(
		options.TLSHandshakeTimeout,
		defaultTLSHandshakeTimeout,
	)
	headerTimeout := defaultDuration(
		options.ResponseHeaderTimeout,
		defaultResponseHeaderTimeout,
	)
	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    options.RootCAs,
	}
	if pin != "" {
		tlsConfig.VerifyConnection = func(
			state tls.ConnectionState,
		) error {
			if len(state.PeerCertificates) == 0 {
				return ErrHTTPSRequest
			}
			digest := sha256.Sum256(
				state.PeerCertificates[0].RawSubjectPublicKeyInfo,
			)
			if hex.EncodeToString(digest[:]) != pin {
				return ErrHTTPSRequest
			}
			return nil
		}
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: dialTimeout}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          8,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   handshakeTimeout,
		ResponseHeaderTimeout: headerTimeout,
		ExpectContinueTimeout: time.Second,
		TLSClientConfig:       tlsConfig,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(
			_ *http.Request,
			_ []*http.Request,
		) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

func (transport *HTTPSTransport) String() string {
	return "remote.HTTPSTransport{Endpoint:<redacted>}"
}

func (transport *HTTPSTransport) GoString() string {
	return transport.String()
}

func (transport *HTTPSTransport) Send(
	ctx context.Context,
	request relay.ForwardRequest,
) (relay.ForwardACK, error) {
	if ctx == nil {
		return relay.ForwardACK{}, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return relay.ForwardACK{}, err
	}
	if transport == nil || transport.endpoint == nil || transport.client == nil {
		return relay.ForwardACK{}, ErrHTTPSRequest
	}
	ingress, err := request.ToIngressRequest()
	if err != nil ||
		ingress.Method != http.MethodPost ||
		ingress.Target != transport.endpoint.EscapedPath() {
		return relay.ForwardACK{}, ErrHTTPSRequest
	}
	httpRequest, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		transport.endpoint.String(),
		bytes.NewReader(request.ExactBody),
	)
	if err != nil {
		return relay.ForwardACK{}, ErrHTTPSRequest
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set(relay.HeaderKeyID, ingress.KeyID)
	httpRequest.Header.Set(
		relay.HeaderTimestamp,
		ingress.SentAt.UTC().Format(time.RFC3339Nano),
	)
	httpRequest.Header.Set(relay.HeaderNonce, ingress.Nonce)
	httpRequest.Header.Set(
		relay.HeaderSignature,
		base64.RawURLEncoding.EncodeToString(ingress.Signature),
	)
	response, err := transport.client.Do(httpRequest)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return relay.ForwardACK{}, ctxErr
		}
		return relay.ForwardACK{}, ErrHTTPSRequest
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK &&
		response.StatusCode != http.StatusAccepted {
		return relay.ForwardACK{}, ErrHTTPSResponse
	}
	mediaType, _, err := mime.ParseMediaType(
		response.Header.Get("Content-Type"),
	)
	if err != nil || mediaType != "application/json" {
		return relay.ForwardACK{}, ErrHTTPSResponse
	}
	body, err := io.ReadAll(
		io.LimitReader(response.Body, maxHTTPSResponseBytes+1),
	)
	if err != nil || len(body) == 0 || len(body) > maxHTTPSResponseBytes {
		return relay.ForwardACK{}, ErrHTTPSResponse
	}
	ingressACK, err := decodeIngressACK(body)
	if err != nil {
		return relay.ForwardACK{}, ErrHTTPSResponse
	}
	ack, err := relay.NewForwardACK(request, ingressACK, transport.time())
	if err != nil {
		return relay.ForwardACK{}, ErrHTTPSResponse
	}
	return ack, nil
}

func (transport *HTTPSTransport) Close() error {
	if transport == nil || transport.client == nil {
		return nil
	}
	if closer, ok := transport.client.Transport.(interface {
		CloseIdleConnections()
	}); ok {
		closer.CloseIdleConnections()
	}
	return nil
}

func (transport *HTTPSTransport) time() time.Time {
	if transport.now != nil {
		return transport.now().UTC()
	}
	return time.Now().UTC()
}

func decodeIngressACK(body []byte) (relay.IngressACK, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	opening, ok := token.(json.Delim)
	if err != nil || !ok || opening != '{' {
		return relay.IngressACK{}, ErrHTTPSResponse
	}
	var receiptID *string
	var localQueueID *string
	var duplicate *bool
	seen := make(map[string]bool, 3)
	for decoder.More() {
		keyToken, err := decoder.Token()
		key, ok := keyToken.(string)
		if err != nil || !ok || seen[key] {
			return relay.IngressACK{}, ErrHTTPSResponse
		}
		seen[key] = true
		switch key {
		case "receiptId":
			var value string
			if decoder.Decode(&value) != nil {
				return relay.IngressACK{}, ErrHTTPSResponse
			}
			receiptID = &value
		case "localQueueId":
			var value string
			if decoder.Decode(&value) != nil {
				return relay.IngressACK{}, ErrHTTPSResponse
			}
			localQueueID = &value
		case "duplicate":
			var value bool
			if decoder.Decode(&value) != nil {
				return relay.IngressACK{}, ErrHTTPSResponse
			}
			duplicate = &value
		default:
			return relay.IngressACK{}, ErrHTTPSResponse
		}
	}
	if _, err := decoder.Token(); err != nil {
		return relay.IngressACK{}, ErrHTTPSResponse
	}
	if receiptID == nil || localQueueID == nil || duplicate == nil {
		return relay.IngressACK{}, ErrHTTPSResponse
	}
	if token, err := decoder.Token(); err != io.EOF || token != nil {
		return relay.IngressACK{}, ErrHTTPSResponse
	}
	return relay.IngressACK{
		ReceiptID:    *receiptID,
		LocalQueueID: *localQueueID,
		Duplicate:    *duplicate,
	}, nil
}

func defaultDuration(value, fallback time.Duration) time.Duration {
	if value == 0 {
		return fallback
	}
	return value
}

var _ relay.ForwardTransport = (*HTTPSTransport)(nil)
