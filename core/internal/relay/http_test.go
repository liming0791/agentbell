package relay

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHTTPIngressAuthenticatesCommitsAndDeduplicates(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	envelope := validEnvelope(t)
	body := encodedEnvelope(t, envelope)
	signature, err := Sign(
		privateKey,
		http.MethodPost,
		"/v1/events",
		envelope.SentAt,
		envelope.Nonce,
		body,
	)
	if err != nil {
		t.Fatal(err)
	}
	nonces, err := OpenNonceStore(t.TempDir()+"/nonces", MinimumNonceRetention)
	if err != nil {
		t.Fatal(err)
	}
	receipts, err := OpenReceiptStore(t.TempDir() + "/receipts")
	if err != nil {
		t.Fatal(err)
	}
	queue := &ingressQueue{}
	handler := NewHTTPHandler(Ingress{
		Peer: func(keyID string) (Peer, bool) {
			return Peer{
				ID:              keyID,
				TeamID:          envelope.TeamID,
				OriginID:        envelope.Origin.ID,
				PublicKey:       publicKey,
				Scopes:          []string{ScopeIngest},
				AllowedSources:  []string{envelope.Event.Source},
				AllowedRuntimes: []string{envelope.Event.Runtime},
			}, keyID == "peer-http"
		},
		Nonces: nonces, Receipts: receipts, Queue: queue,
		Now: func() time.Time { return envelope.SentAt.Add(time.Minute) },
	})
	call := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(
			http.MethodPost,
			"/v1/events",
			bytes.NewReader(body),
		)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set(HeaderKeyID, "peer-http")
		request.Header.Set(
			HeaderTimestamp,
			envelope.SentAt.Format(time.RFC3339Nano),
		)
		request.Header.Set(HeaderNonce, envelope.Nonce)
		request.Header.Set(
			HeaderSignature,
			base64.RawURLEncoding.EncodeToString(signature),
		)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		return response
	}

	first := call()
	if first.Code != http.StatusAccepted {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body)
	}
	var firstACK IngressACK
	if err := json.Unmarshal(first.Body.Bytes(), &firstACK); err != nil {
		t.Fatal(err)
	}
	second := call()
	if second.Code != http.StatusOK {
		t.Fatalf("duplicate status=%d body=%s", second.Code, second.Body)
	}
	var secondACK IngressACK
	if err := json.Unmarshal(second.Body.Bytes(), &secondACK); err != nil {
		t.Fatal(err)
	}
	if !secondACK.Duplicate ||
		secondACK.ReceiptID != firstACK.ReceiptID ||
		queue.calls != 1 {
		t.Fatalf(
			"unstable HTTP ACK: first=%#v second=%#v calls=%d",
			firstACK,
			secondACK,
			queue.calls,
		)
	}
}

func TestHTTPIngressRejectsUnauthenticatedAndOversizedRequests(t *testing.T) {
	handler := NewHTTPHandler(Ingress{})
	for _, test := range []struct {
		name        string
		method      string
		contentType string
		body        []byte
		want        int
	}{
		{
			name: "method", method: http.MethodGet,
			contentType: "application/json", body: []byte(`{}`),
			want: http.StatusMethodNotAllowed,
		},
		{
			name: "content type", method: http.MethodPost,
			contentType: "text/plain", body: []byte(`{}`),
			want: http.StatusUnsupportedMediaType,
		},
		{
			name: "oversized", method: http.MethodPost,
			contentType: "application/json",
			body:        bytes.Repeat([]byte("x"), MaxBodyBytes+1),
			want:        http.StatusRequestEntityTooLarge,
		},
		{
			name: "missing authentication", method: http.MethodPost,
			contentType: "application/json", body: []byte(`{}`),
			want: http.StatusUnauthorized,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(
				test.method,
				"/v1/events",
				bytes.NewReader(test.body),
			)
			request.Header.Set("Content-Type", test.contentType)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf(
					"status=%d want=%d body=%s",
					response.Code,
					test.want,
					response.Body,
				)
			}
		})
	}
}
