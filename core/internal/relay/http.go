package relay

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
)

const (
	HeaderKeyID     = "X-AgentBell-Key-Id"
	HeaderTimestamp = "X-AgentBell-Timestamp"
	HeaderNonce     = "X-AgentBell-Nonce"
	HeaderSignature = "X-AgentBell-Signature"
)

type httpHandler struct {
	ingress Ingress
}

func NewHTTPHandler(ingress Ingress) http.Handler {
	return httpHandler{ingress: ingress}
}

func (handler httpHandler) ServeHTTP(
	response http.ResponseWriter,
	request *http.Request,
) {
	response.Header().Set("Cache-Control", "no-store")
	response.Header().Set("Content-Type", "application/json")
	if request.Method != http.MethodPost {
		response.Header().Set("Allow", http.MethodPost)
		writeHTTPError(response, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if request.URL.Path != "/v1/events" || request.URL.RawQuery != "" {
		writeHTTPError(response, http.StatusNotFound, "not found")
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeHTTPError(
			response,
			http.StatusUnsupportedMediaType,
			"content type must be application/json",
		)
		return
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, MaxBodyBytes+1))
	if err != nil {
		writeHTTPError(response, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(body) > MaxBodyBytes {
		writeHTTPError(
			response,
			http.StatusRequestEntityTooLarge,
			"request body is too large",
		)
		return
	}
	keyID := request.Header.Get(HeaderKeyID)
	timestampValue := request.Header.Get(HeaderTimestamp)
	nonce := request.Header.Get(HeaderNonce)
	signatureValue := request.Header.Get(HeaderSignature)
	if strings.TrimSpace(keyID) == "" ||
		timestampValue == "" ||
		nonce == "" ||
		signatureValue == "" {
		writeHTTPError(response, http.StatusUnauthorized, "unauthorized")
		return
	}
	sentAt, err := time.Parse(time.RFC3339Nano, timestampValue)
	if err != nil {
		writeHTTPError(response, http.StatusUnauthorized, "unauthorized")
		return
	}
	signature, err := base64.RawURLEncoding.DecodeString(signatureValue)
	if err != nil {
		writeHTTPError(response, http.StatusUnauthorized, "unauthorized")
		return
	}
	ack, err := handler.ingress.Accept(IngressRequest{
		KeyID:     keyID,
		Method:    request.Method,
		Target:    request.URL.EscapedPath(),
		SentAt:    sentAt,
		Nonce:     nonce,
		ExactBody: body,
		Signature: signature,
	})
	if err != nil {
		// Authentication, scope, replay, malformed body and durable backend
		// errors intentionally share one response. This endpoint never returns
		// raw crypto, peer, queue or body diagnostics to an untrusted caller.
		writeHTTPError(response, http.StatusUnauthorized, "unauthorized")
		return
	}
	status := http.StatusAccepted
	if ack.Duplicate {
		status = http.StatusOK
	}
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(ack)
}

func writeHTTPError(
	response http.ResponseWriter,
	status int,
	message string,
) {
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(map[string]string{"error": message})
}
