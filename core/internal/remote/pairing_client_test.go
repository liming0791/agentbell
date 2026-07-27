package remote

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPairingClientSendsStrictRequestAndDecodesOnlyPolicy(t *testing.T) {
	publicKey := ed25519.PublicKey(
		bytes.Repeat([]byte{0x42}, ed25519.PublicKeySize),
	)
	var requestBody map[string]json.RawMessage
	server := httptest.NewTLSServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		if request.Method != http.MethodPost ||
			request.URL.EscapedPath() != "/v1/pair" ||
			request.URL.RawQuery != "" {
			t.Errorf("request target = %s %s", request.Method, request.URL)
		}
		if request.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content type = %q", request.Header.Get("Content-Type"))
		}
		if request.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("cache control = %q", request.Header.Get("Cache-Control"))
		}
		body, _ := io.ReadAll(request.Body)
		if err := json.Unmarshal(body, &requestBody); err != nil {
			t.Error(err)
		}
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(response,
			`{"peerId":"peer-one","teamId":"team-main",`+
				`"allowedSources":["codex"],`+
				`"allowedRuntimes":["ssh"]}`,
		)
	}))
	defer server.Close()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	client, err := NewPairingClient(PairingClientConfig{
		Endpoint:   server.URL + "/v1/pair",
		PinnedSPKI: pairingCertificateSPKI(server.Certificate()),
	}, PairingClientOptions{
		RootCAs: roots,
		Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	request := PairingRequest{
		Code:      "  agbr-01234567-89abcdef-ghjkmnpq-rstvwxyz\n",
		PeerID:    "peer-one",
		OriginID:  "origin-one",
		PublicKey: publicKey,
	}
	result, err := client.Pair(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.PeerID != "peer-one" ||
		result.TeamID != "team-main" ||
		len(result.AllowedSources) != 1 ||
		result.AllowedSources[0] != "codex" ||
		len(result.AllowedRuntimes) != 1 ||
		result.AllowedRuntimes[0] != "ssh" {
		t.Fatalf("result = %#v", result)
	}
	if len(requestBody) != 4 ||
		string(requestBody["code"]) !=
			`"AGBR-01234567-89ABCDEF-GHJKMNPQ-RSTVWXYZ"` ||
		string(requestBody["peerId"]) != `"peer-one"` ||
		string(requestBody["originId"]) != `"origin-one"` ||
		string(requestBody["publicKey"]) != `"`+
			base64.RawURLEncoding.EncodeToString(publicKey)+`"` {
		t.Fatalf("request body = %#v", requestBody)
	}
	for _, forbidden := range []string{
		"privateKey",
		"version",
		"runtime",
		"teamId",
	} {
		if _, present := requestBody[forbidden]; present {
			t.Fatalf("request exposed field %q", forbidden)
		}
	}
	for _, rendered := range []string{
		request.String(),
		request.GoString(),
		client.String(),
		client.GoString(),
		(PairingClientConfig{
			Endpoint:   server.URL,
			PinnedSPKI: pairingCertificateSPKI(server.Certificate()),
		}).String(),
		(PairingClientConfig{
			Endpoint:   server.URL,
			PinnedSPKI: pairingCertificateSPKI(server.Certificate()),
		}).GoString(),
	} {
		if containsAny(
			rendered,
			request.Code,
			request.PeerID,
			request.OriginID,
			server.URL,
			pairingCertificateSPKI(server.Certificate()),
			base64.RawURLEncoding.EncodeToString(publicKey),
		) {
			t.Fatalf("formatter leaked pairing material: %s", rendered)
		}
	}
}

func TestPairingClientAllowsOnlyExplicitLoopbackSSHTunnelHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		response http.ResponseWriter,
		_ *http.Request,
	) {
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(response,
			`{"peerId":"peer-one","teamId":"team-main",`+
				`"allowedSources":["codex"],"allowedRuntimes":["ssh"]}`,
		)
	}))
	defer server.Close()
	config := PairingClientConfig{
		Endpoint:  server.URL + "/v1/pair",
		SSHTunnel: true,
	}
	client, err := NewPairingClient(config, PairingClientOptions{
		Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Pair(
		context.Background(),
		validPairingRequest(),
	); err != nil {
		t.Fatal(err)
	}

	config.SSHTunnel = false
	if _, err := NewPairingClient(config, PairingClientOptions{}); !errors.Is(
		err,
		ErrInvalidPairingClient,
	) {
		t.Fatalf("implicit HTTP error = %v", err)
	}
	config.SSHTunnel = true
	config.Endpoint = "http://relay.example.com/v1/pair"
	if _, err := NewPairingClient(config, PairingClientOptions{}); !errors.Is(
		err,
		ErrInvalidPairingClient,
	) {
		t.Fatalf("non-loopback tunnel error = %v", err)
	}
	config.Endpoint = server.URL + "/v1/pair"
	config.PinnedSPKI = strings.Repeat("0", 64)
	if _, err := NewPairingClient(config, PairingClientOptions{}); !errors.Is(
		err,
		ErrInvalidPairingClient,
	) {
		t.Fatalf("HTTP pin error = %v", err)
	}
}

func TestPairingClientRejectsUnsafeConfigurationAndRequest(t *testing.T) {
	configurations := []PairingClientConfig{
		{},
		{Endpoint: "https://user:secret@relay.example.com/v1/pair"},
		{Endpoint: "https://relay.example.com/v1/pair?code=secret"},
		{Endpoint: "https://relay.example.com/v1/%70air"},
		{Endpoint: "https://relay.example.com/other"},
		{
			Endpoint:   "https://relay.example.com/v1/pair",
			PinnedSPKI: strings.Repeat("A", 64),
		},
	}
	for _, config := range configurations {
		if _, err := NewPairingClient(config, PairingClientOptions{}); !errors.Is(
			err,
			ErrInvalidPairingClient,
		) {
			t.Fatalf("config=%#v error=%v", config, err)
		} else if containsAny(err.Error(), config.Endpoint, "secret") {
			t.Fatalf("configuration leaked in error: %v", err)
		}
	}
	if _, err := NewPairingClient(PairingClientConfig{
		Endpoint: "https://relay.example.com/v1/pair",
	}, PairingClientOptions{
		Timeout: -time.Second,
	}); !errors.Is(err, ErrInvalidPairingClientOptions) {
		t.Fatalf("options error = %v", err)
	}

	client, err := NewPairingClient(PairingClientConfig{
		Endpoint: "https://relay.example.com/v1/pair",
	}, PairingClientOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	tests := []PairingRequest{
		{},
		{
			Code:      "not-a-code",
			PeerID:    "peer-one",
			OriginID:  "origin-one",
			PublicKey: bytes.Repeat([]byte{1}, ed25519.PublicKeySize),
		},
		{
			Code:      "AGBR-01234567-89ABCDEF-GHJKMNPQ-RSTVWXYZ",
			PeerID:    "../peer",
			OriginID:  "origin-one",
			PublicKey: bytes.Repeat([]byte{1}, ed25519.PublicKeySize),
		},
		{
			Code:      "AGBR-01234567-89ABCDEF-GHJKMNPQ-RSTVWXYZ",
			PeerID:    "peer-one",
			OriginID:  "origin one",
			PublicKey: bytes.Repeat([]byte{1}, ed25519.PublicKeySize),
		},
		{
			Code:      "AGBR-01234567-89ABCDEF-GHJKMNPQ-RSTVWXYZ",
			PeerID:    "peer-one",
			OriginID:  "origin-one",
			PublicKey: []byte("short"),
		},
	}
	for _, request := range tests {
		if _, err := client.Pair(
			context.Background(),
			request,
		); !errors.Is(err, ErrInvalidPairingRequest) {
			t.Fatalf("request=%#v error=%v", request, err)
		}
	}
	if _, err := client.Pair(nil, validPairingRequest()); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("nil context error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Pair(ctx, validPairingRequest()); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("canceled context error = %v", err)
	}
}

func TestPairingClientRejectsSPKIMismatchRedirectTimeoutAndSecretErrors(t *testing.T) {
	request := validPairingRequest()
	tests := []struct {
		name     string
		handler  http.Handler
		pin      string
		timeout  time.Duration
		expected error
	}{
		{
			name: "SPKI mismatch",
			handler: http.HandlerFunc(func(
				response http.ResponseWriter,
				_ *http.Request,
			) {
				response.WriteHeader(http.StatusCreated)
			}),
			pin:      strings.Repeat("0", 64),
			timeout:  time.Second,
			expected: ErrPairingRequest,
		},
		{
			name: "redirect",
			handler: http.HandlerFunc(func(
				response http.ResponseWriter,
				httpRequest *http.Request,
			) {
				http.Redirect(response, httpRequest, "/private", http.StatusFound)
			}),
			timeout:  time.Second,
			expected: ErrPairingResponse,
		},
		{
			name: "timeout",
			handler: http.HandlerFunc(func(
				response http.ResponseWriter,
				_ *http.Request,
			) {
				time.Sleep(50 * time.Millisecond)
				response.WriteHeader(http.StatusCreated)
			}),
			timeout:  10 * time.Millisecond,
			expected: ErrPairingRequest,
		},
		{
			name: "secret error body",
			handler: http.HandlerFunc(func(
				response http.ResponseWriter,
				_ *http.Request,
			) {
				response.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(
					response,
					"code and private-key diagnostics",
				)
			}),
			timeout:  time.Second,
			expected: ErrPairingResponse,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var redirected atomic.Bool
			server := httptest.NewTLSServer(http.HandlerFunc(func(
				response http.ResponseWriter,
				httpRequest *http.Request,
			) {
				if httpRequest.URL.Path == "/private" {
					redirected.Store(true)
				}
				test.handler.ServeHTTP(response, httpRequest)
			}))
			defer server.Close()
			roots := x509.NewCertPool()
			roots.AddCert(server.Certificate())
			client, err := NewPairingClient(PairingClientConfig{
				Endpoint:   server.URL + "/v1/pair",
				PinnedSPKI: test.pin,
			}, PairingClientOptions{
				RootCAs: roots,
				Timeout: test.timeout,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			_, err = client.Pair(context.Background(), request)
			if !errors.Is(err, test.expected) {
				t.Fatalf("error = %v", err)
			}
			if redirected.Load() {
				t.Fatal("pairing request followed redirect")
			}
			if containsAny(
				err.Error(),
				request.Code,
				base64.RawURLEncoding.EncodeToString(request.PublicKey),
				server.URL,
				"private-key diagnostics",
			) {
				t.Fatalf("pairing error leaked data: %v", err)
			}
		})
	}
}

func TestPairingClientStrictlyBoundsAndValidatesSuccessResponse(t *testing.T) {
	request := validPairingRequest()
	tests := []struct {
		name string
		body string
	}{
		{name: "empty", body: ""},
		{name: "oversized", body: strings.Repeat("x", maxPairingResponseBytes+1)},
		{
			name: "unknown",
			body: `{"peerId":"peer-one","teamId":"team-main",` +
				`"allowedSources":["codex"],"allowedRuntimes":["ssh"],` +
				`"code":"secret"}`,
		},
		{
			name: "duplicate",
			body: `{"peerId":"peer-one","peerId":"peer-one",` +
				`"teamId":"team-main","allowedSources":["codex"],` +
				`"allowedRuntimes":["ssh"]}`,
		},
		{
			name: "peer mismatch",
			body: `{"peerId":"peer-other","teamId":"team-main",` +
				`"allowedSources":["codex"],"allowedRuntimes":["ssh"]}`,
		},
		{
			name: "invalid source",
			body: `{"peerId":"peer-one","teamId":"team-main",` +
				`"allowedSources":["future"],"allowedRuntimes":["ssh"]}`,
		},
		{
			name: "invalid runtime",
			body: `{"peerId":"peer-one","teamId":"team-main",` +
				`"allowedSources":["codex"],"allowedRuntimes":["host"]}`,
		},
		{
			name: "missing field",
			body: `{"peerId":"peer-one","teamId":"team-main"}`,
		},
		{
			name: "trailing JSON",
			body: `{"peerId":"peer-one","teamId":"team-main",` +
				`"allowedSources":["codex"],"allowedRuntimes":["ssh"]}{}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(
				response http.ResponseWriter,
				_ *http.Request,
			) {
				response.Header().Set("Content-Type", "application/json")
				response.WriteHeader(http.StatusCreated)
				_, _ = io.WriteString(response, test.body)
			}))
			defer server.Close()
			roots := x509.NewCertPool()
			roots.AddCert(server.Certificate())
			client, err := NewPairingClient(PairingClientConfig{
				Endpoint: server.URL + "/v1/pair",
			}, PairingClientOptions{
				RootCAs: roots,
				Timeout: time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			if _, err := client.Pair(
				context.Background(),
				request,
			); !errors.Is(err, ErrPairingResponse) {
				t.Fatalf("body=%q error=%v", test.body, err)
			}
		})
	}
}

func validPairingRequest() PairingRequest {
	return PairingRequest{
		Code:     "AGBR-01234567-89ABCDEF-GHJKMNPQ-RSTVWXYZ",
		PeerID:   "peer-one",
		OriginID: "origin-one",
		PublicKey: ed25519.PublicKey(
			bytes.Repeat([]byte{0x12}, ed25519.PublicKeySize),
		),
	}
}

func pairingCertificateSPKI(certificate *x509.Certificate) string {
	digest := sha256.Sum256(certificate.RawSubjectPublicKeyInfo)
	return hex.EncodeToString(digest[:])
}
