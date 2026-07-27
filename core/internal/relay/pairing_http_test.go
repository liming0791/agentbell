package relay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestPairingHTTPEnrollsStrictRequestAndReturnsOnlyPolicy(t *testing.T) {
	publicKey := bytes.Repeat([]byte{0x42}, ed25519.PublicKeySize)
	code := "AGBR-01234567-89ABCDEF-GHJKMNPQ-RSTVWXYZ"
	var captured PairEnrollmentRequest
	handler := NewPairingHTTPHandler(func(
		ctx context.Context,
		request PairEnrollmentRequest,
	) (PairEnrollmentResult, error) {
		if ctx == nil {
			t.Fatal("request context was not passed to Enroll")
		}
		captured = request
		return PairEnrollmentResult{
			PeerID:          request.PeerID,
			TeamID:          "team-main",
			AllowedSources:  []string{"codex", "claude"},
			AllowedRuntimes: []string{"ssh", "container"},
		}, nil
	})
	body := fmt.Sprintf(
		`{"code":%q,"peerId":"peer-one","originId":"origin-one","publicKey":%q}`,
		code,
		base64.RawURLEncoding.EncodeToString(publicKey),
	)
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/pair",
		strings.NewReader(body),
	)
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	if response.Header().Get("Cache-Control") != "no-store" ||
		response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("headers = %#v", response.Header())
	}
	if captured.Code != code ||
		captured.PeerID != "peer-one" ||
		captured.OriginID != "origin-one" ||
		!bytes.Equal(captured.PublicKey, publicKey) {
		t.Fatalf("captured request = %#v", captured)
	}
	if strings.Contains(captured.String(), code) ||
		strings.Contains(captured.GoString(), "peer-one") ||
		strings.Contains(captured.String(), base64.RawURLEncoding.EncodeToString(publicKey)) {
		t.Fatalf("request formatter leaked enrollment material: %s", captured)
	}

	var result map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"code",
		"originId",
		"publicKey",
		"claimId",
		"privateKey",
	} {
		if _, present := result[forbidden]; present {
			t.Fatalf("response exposed %q: %s", forbidden, response.Body)
		}
	}
	if len(result) != 4 ||
		string(result["peerId"]) != `"peer-one"` ||
		string(result["teamId"]) != `"team-main"` {
		t.Fatalf("response shape = %s", response.Body)
	}
}

func TestPairingHTTPRejectsMalformedUnknownDuplicateAndOversizedBodies(t *testing.T) {
	publicKey := base64.RawURLEncoding.EncodeToString(
		bytes.Repeat([]byte{0x42}, ed25519.PublicKeySize),
	)
	valid := fmt.Sprintf(
		`{"code":"AGBR-01234567-89ABCDEF-GHJKMNPQ-RSTVWXYZ","peerId":"peer-one","originId":"origin-one","publicKey":%q}`,
		publicKey,
	)
	tests := []struct {
		name        string
		method      string
		target      string
		contentType string
		body        string
		status      int
	}{
		{
			name: "method", method: http.MethodGet, target: "/v1/pair",
			contentType: "application/json", body: valid,
			status: http.StatusMethodNotAllowed,
		},
		{
			name: "path", method: http.MethodPost, target: "/v1/other",
			contentType: "application/json", body: valid,
			status: http.StatusNotFound,
		},
		{
			name: "query", method: http.MethodPost, target: "/v1/pair?code=secret",
			contentType: "application/json", body: valid,
			status: http.StatusNotFound,
		},
		{
			name: "content type", method: http.MethodPost, target: "/v1/pair",
			contentType: "text/plain", body: valid,
			status: http.StatusUnsupportedMediaType,
		},
		{
			name: "empty", method: http.MethodPost, target: "/v1/pair",
			contentType: "application/json", body: "",
			status: http.StatusBadRequest,
		},
		{
			name: "array", method: http.MethodPost, target: "/v1/pair",
			contentType: "application/json", body: "[]",
			status: http.StatusBadRequest,
		},
		{
			name: "unknown", method: http.MethodPost, target: "/v1/pair",
			contentType: "application/json",
			body:        strings.TrimSuffix(valid, "}") + `,"privateKey":"secret"}`,
			status:      http.StatusBadRequest,
		},
		{
			name: "duplicate", method: http.MethodPost, target: "/v1/pair",
			contentType: "application/json",
			body: strings.Replace(
				valid,
				`"peerId":"peer-one"`,
				`"peerId":"peer-one","peerId":"peer-two"`,
				1,
			),
			status: http.StatusBadRequest,
		},
		{
			name: "trailing JSON", method: http.MethodPost, target: "/v1/pair",
			contentType: "application/json", body: valid + `{}`,
			status: http.StatusBadRequest,
		},
		{
			name: "missing field", method: http.MethodPost, target: "/v1/pair",
			contentType: "application/json",
			body:        `{"code":"AGBR-01234567-89ABCDEF-GHJKMNPQ-RSTVWXYZ"}`,
			status:      http.StatusBadRequest,
		},
		{
			name: "invalid code", method: http.MethodPost, target: "/v1/pair",
			contentType: "application/json",
			body:        strings.Replace(valid, "AGBR-01234567-89ABCDEF-GHJKMNPQ-RSTVWXYZ", "secret-code", 1),
			status:      http.StatusBadRequest,
		},
		{
			name: "invalid peer", method: http.MethodPost, target: "/v1/pair",
			contentType: "application/json",
			body:        strings.Replace(valid, "peer-one", "../peer", 1),
			status:      http.StatusBadRequest,
		},
		{
			name: "invalid origin", method: http.MethodPost, target: "/v1/pair",
			contentType: "application/json",
			body:        strings.Replace(valid, "origin-one", "origin one", 1),
			status:      http.StatusBadRequest,
		},
		{
			name: "padded key", method: http.MethodPost, target: "/v1/pair",
			contentType: "application/json",
			body:        strings.Replace(valid, publicKey, publicKey+"=", 1),
			status:      http.StatusBadRequest,
		},
		{
			name: "short key", method: http.MethodPost, target: "/v1/pair",
			contentType: "application/json",
			body: strings.Replace(
				valid,
				publicKey,
				base64.RawURLEncoding.EncodeToString([]byte("short")),
				1,
			),
			status: http.StatusBadRequest,
		},
		{
			name: "oversized", method: http.MethodPost, target: "/v1/pair",
			contentType: "application/json",
			body:        strings.Repeat("x", MaxBodyBytes+1),
			status:      http.StatusRequestEntityTooLarge,
		},
	}
	var calls atomic.Int32
	handler := NewPairingHTTPHandler(func(
		context.Context,
		PairEnrollmentRequest,
	) (PairEnrollmentResult, error) {
		calls.Add(1)
		return PairEnrollmentResult{}, errors.New("must not be called")
	})
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(
				test.method,
				test.target,
				strings.NewReader(test.body),
			)
			request.Header.Set("Content-Type", test.contentType)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf(
					"status=%d want=%d body=%s",
					response.Code,
					test.status,
					response.Body,
				)
			}
			assertPairingFailureIsSanitized(t, response)
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("cache control = %q", response.Header().Get("Cache-Control"))
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("Enroll called %d times for invalid requests", calls.Load())
	}
}

func TestPairingHTTPUnifiesEnrollmentFailuresAndRejectsUnsafeResults(t *testing.T) {
	publicKey := base64.RawURLEncoding.EncodeToString(
		bytes.Repeat([]byte{0x11}, ed25519.PublicKeySize),
	)
	body := fmt.Sprintf(
		`{"code":"AGBR-01234567-89ABCDEF-GHJKMNPQ-RSTVWXYZ","peerId":"peer-one","originId":"origin-one","publicKey":%q}`,
		publicKey,
	)
	secret := "claim-id-and-code-secret"
	tests := []struct {
		name   string
		enroll PairEnrollmentFunc
		status int
	}{
		{
			name: "callback error",
			enroll: func(context.Context, PairEnrollmentRequest) (
				PairEnrollmentResult,
				error,
			) {
				return PairEnrollmentResult{}, errors.New(secret)
			},
			status: http.StatusUnauthorized,
		},
		{
			name: "callback panic",
			enroll: func(context.Context, PairEnrollmentRequest) (
				PairEnrollmentResult,
				error,
			) {
				panic(secret)
			},
			status: http.StatusInternalServerError,
		},
		{
			name: "peer mismatch",
			enroll: func(context.Context, PairEnrollmentRequest) (
				PairEnrollmentResult,
				error,
			) {
				return PairEnrollmentResult{
					PeerID:          "peer-other",
					TeamID:          "team-main",
					AllowedSources:  []string{"codex"},
					AllowedRuntimes: []string{"ssh"},
				}, nil
			},
			status: http.StatusInternalServerError,
		},
		{
			name: "invalid policy",
			enroll: func(context.Context, PairEnrollmentRequest) (
				PairEnrollmentResult,
				error,
			) {
				return PairEnrollmentResult{
					PeerID:          "peer-one",
					TeamID:          "team-main",
					AllowedSources:  []string{"unknown-source"},
					AllowedRuntimes: []string{"host"},
				}, nil
			},
			status: http.StatusInternalServerError,
		},
		{
			name:   "missing callback",
			enroll: nil,
			status: http.StatusServiceUnavailable,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(
				http.MethodPost,
				"/v1/pair",
				strings.NewReader(body),
			)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			NewPairingHTTPHandler(test.enroll).ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body)
			}
			assertPairingFailureIsSanitized(t, response)
			if strings.Contains(response.Body.String(), secret) ||
				strings.Contains(response.Body.String(), publicKey) {
				t.Fatalf("failure leaked enrollment material: %s", response.Body)
			}
		})
	}
}

func TestPairingHTTPConcurrentEnrollmentHasOneWinner(t *testing.T) {
	publicKey := base64.RawURLEncoding.EncodeToString(
		bytes.Repeat([]byte{0x23}, ed25519.PublicKeySize),
	)
	body := fmt.Sprintf(
		`{"code":"AGBR-01234567-89ABCDEF-GHJKMNPQ-RSTVWXYZ","peerId":"peer-one","originId":"origin-one","publicKey":%q}`,
		publicKey,
	)
	var winner atomic.Bool
	handler := NewPairingHTTPHandler(func(
		context.Context,
		PairEnrollmentRequest,
	) (PairEnrollmentResult, error) {
		if !winner.CompareAndSwap(false, true) {
			return PairEnrollmentResult{}, ErrPairingClaimed
		}
		return PairEnrollmentResult{
			PeerID:          "peer-one",
			TeamID:          "team-main",
			AllowedSources:  []string{"codex"},
			AllowedRuntimes: []string{"ssh"},
		}, nil
	})
	const requests = 32
	var wait sync.WaitGroup
	statuses := make(chan int, requests)
	for range requests {
		wait.Add(1)
		go func() {
			defer wait.Done()
			request := httptest.NewRequest(
				http.MethodPost,
				"/v1/pair",
				strings.NewReader(body),
			)
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			statuses <- response.Code
		}()
	}
	wait.Wait()
	close(statuses)
	created := 0
	rejected := 0
	for status := range statuses {
		switch status {
		case http.StatusCreated:
			created++
		case http.StatusUnauthorized:
			rejected++
		default:
			t.Fatalf("unexpected status %d", status)
		}
	}
	if created != 1 || rejected != requests-1 {
		t.Fatalf("created=%d rejected=%d", created, rejected)
	}
}

func TestPairingHTTPBodyReadEdgesRemainBoundedAndSanitized(t *testing.T) {
	handler := NewPairingHTTPHandler(nil)
	tests := []struct {
		name   string
		body   io.ReadCloser
		length int64
		status int
	}{
		{
			name:   "nil body",
			body:   nil,
			length: 0,
			status: http.StatusBadRequest,
		},
		{
			name:   "read failure",
			body:   failingPairingBody{},
			length: -1,
			status: http.StatusBadRequest,
		},
		{
			name: "hidden oversized body",
			body: io.NopCloser(bytes.NewReader(
				bytes.Repeat([]byte("x"), MaxBodyBytes+1),
			)),
			length: -1,
			status: http.StatusRequestEntityTooLarge,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := &http.Request{
				Method:        http.MethodPost,
				URL:           &url.URL{Path: "/v1/pair"},
				Header:        make(http.Header),
				Body:          test.body,
				ContentLength: test.length,
			}
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status=%d body=%s", response.Code, response.Body)
			}
			assertPairingFailureIsSanitized(t, response)
		})
	}
}

type failingPairingBody struct{}

func (failingPairingBody) Read([]byte) (int, error) {
	return 0, errors.New("private body read failure")
}

func (failingPairingBody) Close() error {
	return nil
}

func assertPairingFailureIsSanitized(
	t *testing.T,
	response *httptest.ResponseRecorder,
) {
	t.Helper()
	if response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("content type = %q", response.Header().Get("Content-Type"))
	}
	if response.Body.String() != `{"error":"pairing failed"}`+"\n" {
		t.Fatalf("failure body = %q", response.Body.String())
	}
}
