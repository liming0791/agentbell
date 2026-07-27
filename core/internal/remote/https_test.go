package remote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/liming0791/agentbell/core/internal/relay"
)

func TestHTTPSTransportSendsExactEnvelopeAndAcceptsDurableIngressACK(t *testing.T) {
	request := validForwardRequest(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		httpRequest *http.Request,
	) {
		body, _ := io.ReadAll(httpRequest.Body)
		switch {
		case httpRequest.Method != http.MethodPost:
			t.Errorf("method = %q", httpRequest.Method)
		case httpRequest.URL.Path != "/v1/events":
			t.Errorf("path = %q", httpRequest.URL.Path)
		case !bytes.Equal(body, request.ExactBody):
			t.Error("exact body changed")
		case httpRequest.Header.Get(relay.HeaderKeyID) != request.Signature.KeyID:
			t.Error("key ID header changed")
		case httpRequest.Header.Get(relay.HeaderTimestamp) !=
			request.Signature.SentAt.Format(time.RFC3339Nano):
			t.Error("timestamp header changed")
		case httpRequest.Header.Get(relay.HeaderNonce) != request.Signature.Nonce:
			t.Error("nonce header changed")
		case httpRequest.Header.Get(relay.HeaderSignature) !=
			base64.RawURLEncoding.EncodeToString(request.Signature.Signature):
			t.Error("signature header changed")
		}
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(response,
			`{"receiptId":"`+request.ItemID+
				`","localQueueId":"queue-https","duplicate":false}`,
		)
	}))
	defer server.Close()

	config := validRemoteConfig("https")
	config.Connector.HTTPS.Endpoint = server.URL + "/v1/events"
	config.Connector.HTTPS.PinnedSPKI = certificateSPKI(t, server.Certificate())
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	now := time.Date(2026, 7, 27, 14, 0, 0, 0, time.UTC)
	transport, err := NewHTTPSTransport(config, HTTPSOptions{
		RootCAs: roots,
		Now:     func() time.Time { return now },
		Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	ack, err := transport.Send(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if ack.ItemID != request.ItemID ||
		ack.LocalQueueID != "queue-https" ||
		!ack.Durable ||
		ack.Duplicate ||
		!ack.CommittedAt.Equal(now) {
		t.Fatalf("ACK = %#v", ack)
	}
	if containsAny(transport.String(), server.URL, request.Signature.KeyID) {
		t.Fatalf("transport String leaked configuration: %s", transport)
	}
}

func TestHTTPSTransportEnforcesSPKIStatusAndResponseBounds(t *testing.T) {
	request := validForwardRequest(t)
	tests := []struct {
		name     string
		handler  http.Handler
		pin      string
		expected error
	}{
		{
			name: "wrong SPKI",
			handler: http.HandlerFunc(func(
				response http.ResponseWriter,
				_ *http.Request,
			) {
				response.WriteHeader(http.StatusAccepted)
			}),
			pin:      strings.Repeat("0", 64),
			expected: ErrHTTPSRequest,
		},
		{
			name: "secret server error body",
			handler: http.HandlerFunc(func(
				response http.ResponseWriter,
				_ *http.Request,
			) {
				response.WriteHeader(http.StatusInternalServerError)
				_, _ = io.WriteString(response, "private-key-secret")
			}),
			expected: ErrHTTPSResponse,
		},
		{
			name: "oversized response",
			handler: http.HandlerFunc(func(
				response http.ResponseWriter,
				_ *http.Request,
			) {
				response.Header().Set("Content-Type", "application/json")
				response.WriteHeader(http.StatusAccepted)
				_, _ = io.WriteString(
					response,
					strings.Repeat("x", maxHTTPSResponseBytes+1),
				)
			}),
			expected: ErrHTTPSResponse,
		},
		{
			name: "duplicate response field",
			handler: http.HandlerFunc(func(
				response http.ResponseWriter,
				_ *http.Request,
			) {
				response.Header().Set("Content-Type", "application/json")
				response.WriteHeader(http.StatusAccepted)
				_, _ = io.WriteString(response,
					`{"receiptId":"`+request.ItemID+
						`","receiptId":"`+request.ItemID+
						`","localQueueId":"queue","duplicate":false}`,
				)
			}),
			expected: ErrHTTPSResponse,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(test.handler)
			defer server.Close()
			roots := x509.NewCertPool()
			roots.AddCert(server.Certificate())
			config := validRemoteConfig("https")
			config.Connector.HTTPS.Endpoint = server.URL + "/v1/events"
			if test.pin != "" {
				config.Connector.HTTPS.PinnedSPKI = test.pin
			}
			transport, err := NewHTTPSTransport(config, HTTPSOptions{
				RootCAs: roots,
				Timeout: time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer transport.Close()
			_, err = transport.Send(context.Background(), request)
			if !errors.Is(err, test.expected) {
				t.Fatalf("error = %v", err)
			}
			if containsAny(
				err.Error(),
				"private-key-secret",
				string(request.ExactBody),
				request.Signature.KeyID,
			) {
				t.Fatalf("HTTPS error leaked details: %v", err)
			}
		})
	}
}

func TestHTTPSTransportRejectsUnsafeConfigurationAndCancellation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*remoteConfigAlias)
	}{
		{
			name: "query",
			mutate: func(value *remoteConfigAlias) {
				value.endpoint += "?token=secret"
			},
		},
		{
			name: "wrong path",
			mutate: func(value *remoteConfigAlias) {
				value.endpoint = "https://relay.example.com/not-events"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validRemoteConfig("https")
			alias := remoteConfigAlias{
				endpoint: config.Connector.HTTPS.Endpoint,
			}
			test.mutate(&alias)
			config.Connector.HTTPS.Endpoint = alias.endpoint
			_, err := NewHTTPSTransport(config, HTTPSOptions{})
			if !errors.Is(err, ErrInvalidRemoteConfig) {
				t.Fatalf("error = %v", err)
			}
			if containsAny(err.Error(), "token=secret") {
				t.Fatalf("configuration leaked: %v", err)
			}
		})
	}

	config := validRemoteConfig("wsl")
	if _, err := NewHTTPSTransport(config, HTTPSOptions{}); !errors.Is(
		err,
		ErrNotHTTPSConnector,
	) {
		t.Fatalf("wrong connector error = %v", err)
	}
	config = validRemoteConfig("https")
	if _, err := NewHTTPSTransport(config, HTTPSOptions{
		Timeout: -time.Second,
	}); !errors.Is(err, ErrInvalidHTTPSOptions) {
		t.Fatalf("timeout error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	transport, err := NewHTTPSTransport(config, HTTPSOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.Close()
	if _, err := transport.Send(ctx, validForwardRequest(t)); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("cancellation error = %v", err)
	}
}

type remoteConfigAlias struct {
	endpoint string
}

func certificateSPKI(t *testing.T, certificate *x509.Certificate) string {
	t.Helper()
	sum := sha256.Sum256(certificate.RawSubjectPublicKeyInfo)
	return hex.EncodeToString(sum[:])
}
