package binding

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

const (
	// SearchIdentityUser freezes the only identity permitted for binding-code
	// discovery. A Runner implementation must map this request to the official
	// lark-cli `im +messages-search --as user` operation.
	SearchIdentityUser = "user"

	verificationMessage = "AgentBell channel binding verification"
)

var (
	ErrInvalidDiscovery    = errors.New("invalid binding discovery request")
	ErrMessageSearchFailed = errors.New("binding message search failed")
	ErrNoExactMessage      = errors.New("no exact binding message found")
	ErrMultipleChats       = errors.New("binding message matched multiple chats")
	ErrUnsafeSearchResult  = errors.New("binding message search returned an unsafe result")
	ErrVerificationFailed  = errors.New("binding send permission verification failed")
)

// Runner is the narrow lark-cli protocol boundary used by binding discovery.
//
// Implementations must use the official lark-cli. The request carries the
// complete, exact search constraints, but deliberately does not freeze
// undocumented command-line flags or vendor-private response fields. A runner
// translates verified lark-cli semantics into SearchResult and must not log the
// request, raw command output, token, or chat ID.
type Runner interface {
	SearchMessages(context.Context, SearchRequest) (SearchResult, error)
	SendVerification(context.Context, VerificationRequest) error
}

// SearchRequest describes the exact official lark-cli search the Runner must
// perform. Discovery enforces all constraints again on the returned messages.
type SearchRequest struct {
	ExactText string
	Identity  string
	CreatedAt time.Time
	ExpiresAt time.Time
}

func (request SearchRequest) String() string {
	return fmt.Sprintf(
		"binding.SearchRequest{ExactText:<redacted>, Identity:%q, CreatedAt:%s, ExpiresAt:%s}",
		request.Identity,
		request.CreatedAt.UTC().Format(time.RFC3339Nano),
		request.ExpiresAt.UTC().Format(time.RFC3339Nano),
	)
}

func (request SearchRequest) GoString() string {
	return request.String()
}

// SearchResult is the normalized, minimal output of the official lark-cli
// message search. BodyContent is the raw body.content JSON for a plain-text
// Feishu message, for example {"text":"..."}.
type SearchResult struct {
	Messages []SearchMessage
}

func (result SearchResult) String() string {
	return fmt.Sprintf("binding.SearchResult{Messages:<redacted:%d>}", len(result.Messages))
}

func (result SearchResult) GoString() string {
	return result.String()
}

type SearchMessage struct {
	ChatID      string
	Identity    string
	CreatedAt   time.Time
	BodyContent json.RawMessage
}

func (message SearchMessage) String() string {
	return fmt.Sprintf(
		"binding.SearchMessage{ChatID:<redacted>, Identity:%q, CreatedAt:%s, BodyContent:<redacted>}",
		message.Identity,
		message.CreatedAt.UTC().Format(time.RFC3339Nano),
	)
}

func (message SearchMessage) GoString() string {
	return message.String()
}

// Destination is an opaque chat destination. It intentionally redacts itself
// from all standard formatting; the app transaction layer can retrieve the
// value only when it is ready to persist the verified Channel.
type Destination struct {
	chatID string
}

func newDestination(chatID string) Destination {
	return Destination{chatID: chatID}
}

func (destination Destination) ChatID() string {
	return destination.chatID
}

func (Destination) String() string {
	return "binding.Destination{ChatID:<redacted>}"
}

func (destination Destination) GoString() string {
	return destination.String()
}

type VerificationRequest struct {
	Destination    Destination
	Identity       string
	Text           string
	IdempotencyKey string
}

func (request VerificationRequest) String() string {
	return fmt.Sprintf(
		"binding.VerificationRequest{Destination:%s, Identity:%q, Text:<redacted>, IdempotencyKey:%q}",
		request.Destination,
		request.Identity,
		request.IdempotencyKey,
	)
}

func (request VerificationRequest) GoString() string {
	return request.String()
}

// Candidate is returned only after the target identity has successfully sent
// the verification message. It is safe to render or JSON-encode; the raw chat
// destination remains opaque until explicitly requested by the app transaction
// layer.
type Candidate struct {
	ChannelName string    `json:"channelName"`
	Identity    string    `json:"identity"`
	MatchedAt   time.Time `json:"matchedAt"`
	VerifiedAt  time.Time `json:"verifiedAt"`

	destination Destination
}

func (candidate Candidate) Destination() Destination {
	return candidate.destination
}

func (candidate Candidate) String() string {
	return fmt.Sprintf(
		"binding.Candidate{ChannelName:%q, Identity:%q, Destination:<redacted>, MatchedAt:%s, VerifiedAt:%s}",
		candidate.ChannelName,
		candidate.Identity,
		candidate.MatchedAt.UTC().Format(time.RFC3339Nano),
		candidate.VerifiedAt.UTC().Format(time.RFC3339Nano),
	)
}

func (candidate Candidate) GoString() string {
	return candidate.String()
}

type Discovery struct {
	Runner Runner
	Now    func() time.Time
}

// DiscoverAndVerify selects the unique chat containing the exact standalone
// binding code inside the record's original validity window, then verifies that
// the requested target identity can send there. It does not mutate the binding
// Store or Channel configuration.
func (discovery Discovery) DiscoverAndVerify(
	ctx context.Context,
	code string,
	record Record,
) (Candidate, error) {
	canonical, err := canonicalCode(code)
	if err != nil || !validateDiscoveryRecord(canonical, record) || discovery.Runner == nil {
		return Candidate{}, ErrInvalidDiscovery
	}

	searchRequest := SearchRequest{
		ExactText: canonical,
		Identity:  SearchIdentityUser,
		CreatedAt: record.CreatedAt.UTC(),
		ExpiresAt: record.ExpiresAt.UTC(),
	}
	result, err := discovery.Runner.SearchMessages(ctx, searchRequest)
	if err != nil {
		return Candidate{}, ErrMessageSearchFailed
	}

	destination, matchedAt, err := uniqueExactDestination(
		canonical,
		searchRequest,
		result,
	)
	if err != nil {
		return Candidate{}, err
	}

	verifyRequest := VerificationRequest{
		Destination:    destination,
		Identity:       record.As,
		Text:           verificationMessage,
		IdempotencyKey: verificationIdempotencyKey(record, destination),
	}
	if err := discovery.Runner.SendVerification(ctx, verifyRequest); err != nil {
		return Candidate{}, ErrVerificationFailed
	}

	return Candidate{
		ChannelName: record.ChannelName,
		Identity:    record.As,
		MatchedAt:   matchedAt.UTC(),
		VerifiedAt:  discovery.now().UTC(),
		destination: destination,
	}, nil
}

func (discovery Discovery) now() time.Time {
	if discovery.Now != nil {
		return discovery.Now()
	}
	return time.Now()
}

func validateDiscoveryRecord(code string, record Record) bool {
	expectedHash, _ := bindingHash(code)
	return record.Version == recordVersion &&
		record.CodeHash == expectedHash &&
		record.ChannelName == strings.TrimSpace(record.ChannelName) &&
		len(record.ChannelName) >= 1 &&
		len(record.ChannelName) <= 120 &&
		(record.As == "bot" || record.As == "user") &&
		!record.CreatedAt.IsZero() &&
		!record.ExpiresAt.IsZero() &&
		record.CreatedAt.Before(record.ExpiresAt) &&
		record.ConsumedAt.IsZero()
}

func uniqueExactDestination(
	exactText string,
	request SearchRequest,
	result SearchResult,
) (Destination, time.Time, error) {
	matches := make(map[string]time.Time)
	for _, message := range result.Messages {
		text, ok := exactPlainText(message.BodyContent)
		if !ok || text != exactText ||
			message.Identity != SearchIdentityUser ||
			message.CreatedAt.Before(request.CreatedAt) ||
			!message.CreatedAt.Before(request.ExpiresAt) {
			continue
		}
		if !validChatID(message.ChatID) {
			return Destination{}, time.Time{}, ErrUnsafeSearchResult
		}
		if previous, exists := matches[message.ChatID]; !exists || message.CreatedAt.After(previous) {
			matches[message.ChatID] = message.CreatedAt
		}
	}
	if len(matches) == 0 {
		return Destination{}, time.Time{}, ErrNoExactMessage
	}
	if len(matches) != 1 {
		return Destination{}, time.Time{}, ErrMultipleChats
	}
	for chatID, matchedAt := range matches {
		return newDestination(chatID), matchedAt, nil
	}
	panic("unreachable")
}

func exactPlainText(content json.RawMessage) (string, bool) {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var body struct {
		Text *string `json:"text"`
	}
	if err := decoder.Decode(&body); err != nil || body.Text == nil {
		return "", false
	}
	var trailer any
	if err := decoder.Decode(&trailer); !errors.Is(err, io.EOF) {
		return "", false
	}
	return *body.Text, true
}

func validChatID(value string) bool {
	if value == "" || len(value) > 512 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character <= ' ' || character == '\u007f' {
			return false
		}
	}
	return true
}

func verificationIdempotencyKey(record Record, destination Destination) string {
	hash := sha256.New()
	_, _ = io.WriteString(hash, "agentbell-binding-verification-v1\x00")
	_, _ = io.WriteString(hash, record.CodeHash)
	_, _ = io.WriteString(hash, "\x00")
	_, _ = io.WriteString(hash, destination.ChatID())
	_, _ = io.WriteString(hash, "\x00")
	_, _ = io.WriteString(hash, record.As)
	return "sha256:" + hex.EncodeToString(hash.Sum(nil))
}
