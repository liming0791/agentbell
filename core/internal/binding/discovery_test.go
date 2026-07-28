package binding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

const testBindingCode = "AGB-01234-56789-ABCDE-FGHJK"

type scriptedRunner struct {
	searchResult SearchResult
	searchErr    error
	verifyErr    error
	searchCalls  []SearchRequest
	verifyCalls  []VerificationRequest
}

func (runner *scriptedRunner) SearchMessages(
	_ context.Context,
	request SearchRequest,
) (SearchResult, error) {
	runner.searchCalls = append(runner.searchCalls, request)
	return runner.searchResult, runner.searchErr
}

func (runner *scriptedRunner) SendVerification(
	_ context.Context,
	request VerificationRequest,
) error {
	runner.verifyCalls = append(runner.verifyCalls, request)
	return runner.verifyErr
}

func TestDiscoverAndVerifyReturnsSafeUniqueCandidate(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	record := validDiscoveryRecord(t, now, "bot")
	runner := &scriptedRunner{searchResult: SearchResult{Messages: []SearchMessage{
		{
			ChatID:      "oc_private_destination",
			Identity:    "user",
			CreatedAt:   now.Add(time.Minute),
			BodyContent: json.RawMessage(`{"text":"` + testBindingCode + `"}`),
		},
	}}}
	discovery := Discovery{Runner: runner, Now: func() time.Time {
		return now.Add(2 * time.Minute)
	}}

	candidate, err := discovery.DiscoverAndVerify(
		context.Background(),
		testBindingCode,
		record,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.searchCalls) != 1 {
		t.Fatalf("search calls = %d", len(runner.searchCalls))
	}
	search := runner.searchCalls[0]
	if search.ExactText != testBindingCode ||
		search.Identity != SearchIdentityUser ||
		!search.CreatedAt.Equal(record.CreatedAt) ||
		!search.ExpiresAt.Equal(record.ExpiresAt) {
		t.Fatalf("unsafe or incomplete search request: %#v", search)
	}
	if len(runner.verifyCalls) != 1 {
		t.Fatalf("verification calls = %d", len(runner.verifyCalls))
	}
	verify := runner.verifyCalls[0]
	if verify.Destination.ChatID() != "oc_private_destination" ||
		verify.Identity != "bot" ||
		verify.Text != verificationMessage ||
		!strings.HasPrefix(verify.IdempotencyKey, "sha256:") {
		t.Fatalf("verification request = %#v", verify)
	}
	if candidate.ChannelName != record.ChannelName ||
		candidate.Identity != "bot" ||
		candidate.Destination().ChatID() != "oc_private_destination" ||
		!candidate.MatchedAt.Equal(now.Add(time.Minute)) ||
		!candidate.VerifiedAt.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("candidate = %#v", candidate)
	}

	for _, rendered := range []string{
		fmt.Sprint(candidate),
		fmt.Sprintf("%#v", candidate),
		fmt.Sprint(candidate.Destination()),
		fmt.Sprintf("%#v", candidate.Destination()),
		fmt.Sprint(search),
		fmt.Sprint(runner.searchResult),
		fmt.Sprintf("%#v", runner.searchResult.Messages[0]),
		fmt.Sprintf("%#v", verify),
	} {
		if strings.Contains(rendered, testBindingCode) ||
			strings.Contains(rendered, "oc_private_destination") {
			t.Fatalf("safe formatting leaked a binding secret: %s", rendered)
		}
	}
	encoded, err := json.Marshal(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "oc_private_destination") ||
		strings.Contains(string(encoded), testBindingCode) {
		t.Fatalf("candidate JSON leaked a binding secret: %s", encoded)
	}
}

func TestDiscoverRejectsNonExactMessages(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	record := validDiscoveryRecord(t, now, "user")
	tests := []struct {
		name    string
		content string
	}{
		{"prefix", `{"text":"code: ` + testBindingCode + `"}`},
		{"suffix", `{"text":"` + testBindingCode + ` please"}`},
		{"leading whitespace", `{"text":" ` + testBindingCode + `"}`},
		{"trailing newline", `{"text":"` + testBindingCode + `\n"}`},
		{"lowercase", `{"text":"agb-01234-56789-abcdefg-hjkmn"}`},
		{"rich text", `{"text":"` + testBindingCode + `","tag":"post"}`},
		{"JSON trailer", `{"text":"` + testBindingCode + `"} {}`},
		{"not JSON", testBindingCode},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := &scriptedRunner{searchResult: SearchResult{Messages: []SearchMessage{
				{
					ChatID:      "oc_target",
					Identity:    "user",
					CreatedAt:   now.Add(time.Minute),
					BodyContent: json.RawMessage(test.content),
				},
			}}}
			_, err := (Discovery{Runner: runner}).DiscoverAndVerify(
				context.Background(),
				testBindingCode,
				record,
			)
			if !errors.Is(err, ErrNoExactMessage) {
				t.Fatalf("error = %v", err)
			}
			if len(runner.verifyCalls) != 0 {
				t.Fatal("non-exact message triggered verification")
			}
		})
	}
}

func TestDiscoverRestrictsTimeAndSearchIdentity(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	record := validDiscoveryRecord(t, now, "bot")
	for _, test := range []struct {
		name      string
		createdAt time.Time
		identity  string
	}{
		{"before creation", now.Add(-time.Nanosecond), "user"},
		{"at expiry", record.ExpiresAt, "user"},
		{"after expiry", record.ExpiresAt.Add(time.Nanosecond), "user"},
		{"bot-authored", now.Add(time.Minute), "bot"},
		{"missing identity", now.Add(time.Minute), ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &scriptedRunner{searchResult: SearchResult{Messages: []SearchMessage{
				exactSearchMessage("oc_target", test.identity, test.createdAt),
			}}}
			_, err := (Discovery{Runner: runner}).DiscoverAndVerify(
				context.Background(),
				testBindingCode,
				record,
			)
			if !errors.Is(err, ErrNoExactMessage) {
				t.Fatalf("error = %v", err)
			}
			if len(runner.verifyCalls) != 0 {
				t.Fatal("out-of-scope message triggered verification")
			}
		})
	}
}

func TestDiscoverRejectsMultipleChatsButAllowsRepeatedMessageInOneChat(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	record := validDiscoveryRecord(t, now, "user")
	runner := &scriptedRunner{searchResult: SearchResult{Messages: []SearchMessage{
		exactSearchMessage("oc_first", "user", now.Add(time.Minute)),
		exactSearchMessage("oc_second", "user", now.Add(2*time.Minute)),
	}}}
	_, err := (Discovery{Runner: runner}).DiscoverAndVerify(
		context.Background(),
		testBindingCode,
		record,
	)
	if !errors.Is(err, ErrMultipleChats) {
		t.Fatalf("multiple-chat error = %v", err)
	}
	if len(runner.verifyCalls) != 0 {
		t.Fatal("ambiguous match triggered verification")
	}

	runner = &scriptedRunner{searchResult: SearchResult{Messages: []SearchMessage{
		exactSearchMessage("oc_same", "user", now.Add(time.Minute)),
		exactSearchMessage("oc_same", "user", now.Add(2*time.Minute)),
	}}}
	candidate, err := (Discovery{Runner: runner}).DiscoverAndVerify(
		context.Background(),
		testBindingCode,
		record,
	)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Destination().ChatID() != "oc_same" ||
		!candidate.MatchedAt.Equal(now.Add(2*time.Minute)) {
		t.Fatalf("candidate = %#v", candidate)
	}
}

func TestDiscoverRejectsUnsafeResultAndInvalidBinding(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	record := validDiscoveryRecord(t, now, "bot")
	runner := &scriptedRunner{searchResult: SearchResult{Messages: []SearchMessage{
		exactSearchMessage("", "user", now.Add(time.Minute)),
	}}}
	_, err := (Discovery{Runner: runner}).DiscoverAndVerify(
		context.Background(),
		testBindingCode,
		record,
	)
	if !errors.Is(err, ErrUnsafeSearchResult) {
		t.Fatalf("unsafe result error = %v", err)
	}

	tests := []struct {
		name   string
		code   string
		mutate func(*Record)
	}{
		{"bad code", "not-a-code", func(*Record) {}},
		{"hash mismatch", testBindingCode, func(value *Record) { value.CodeHash = "sha256:wrong" }},
		{"bad identity", testBindingCode, func(value *Record) { value.As = "admin" }},
		{"empty channel", testBindingCode, func(value *Record) { value.ChannelName = "" }},
		{"untrimmed channel", testBindingCode, func(value *Record) { value.ChannelName = " Team" }},
		{"long channel", testBindingCode, func(value *Record) { value.ChannelName = strings.Repeat("x", 121) }},
		{"empty creation", testBindingCode, func(value *Record) { value.CreatedAt = time.Time{} }},
		{"backwards window", testBindingCode, func(value *Record) { value.ExpiresAt = value.CreatedAt }},
		{"consumed", testBindingCode, func(value *Record) { value.ConsumedAt = now }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value := record
			test.mutate(&value)
			_, callErr := (Discovery{Runner: runner}).DiscoverAndVerify(
				context.Background(),
				test.code,
				value,
			)
			if !errors.Is(callErr, ErrInvalidDiscovery) {
				t.Fatalf("error = %v", callErr)
			}
		})
	}
	if _, err := (Discovery{}).DiscoverAndVerify(
		context.Background(),
		testBindingCode,
		record,
	); !errors.Is(err, ErrInvalidDiscovery) {
		t.Fatalf("missing runner error = %v", err)
	}
}

func TestDiscoveryErrorsDoNotLeakRunnerDetails(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	record := validDiscoveryRecord(t, now, "bot")
	for _, test := range []struct {
		name      string
		searchErr error
		verifyErr error
		want      error
	}{
		{
			name:      "search",
			searchErr: errors.New("token=secret-token chat=oc_secret"),
			want:      ErrMessageSearchFailed,
		},
		{
			name:      "verification",
			verifyErr: errors.New("token=secret-token chat=oc_secret"),
			want:      ErrVerificationFailed,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			runner := &scriptedRunner{
				searchResult: SearchResult{Messages: []SearchMessage{
					exactSearchMessage("oc_secret", "user", now.Add(time.Minute)),
				}},
				searchErr: test.searchErr,
				verifyErr: test.verifyErr,
			}
			_, err := (Discovery{Runner: runner}).DiscoverAndVerify(
				context.Background(),
				testBindingCode,
				record,
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v", err)
			}
			if strings.Contains(err.Error(), "secret-token") ||
				strings.Contains(err.Error(), "oc_secret") {
				t.Fatalf("error leaked runner details: %v", err)
			}
		})
	}
}

func TestVerificationIdempotencyKeyIsStableAndDestinationBound(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	record := validDiscoveryRecord(t, now, "bot")
	first := verificationIdempotencyKey(record, newDestination("oc_first"))
	second := verificationIdempotencyKey(record, newDestination("oc_first"))
	other := verificationIdempotencyKey(record, newDestination("oc_second"))
	if first == "" || first != second || first == other ||
		!strings.HasPrefix(first, "sha256:") {
		t.Fatalf("keys first=%q second=%q other=%q", first, second, other)
	}
	for _, value := range []string{first, second, other} {
		if strings.Contains(value, "oc_") ||
			strings.Contains(value, testBindingCode) {
			t.Fatalf("idempotency key leaked input: %q", value)
		}
	}
}

func exactSearchMessage(chatID string, identity string, createdAt time.Time) SearchMessage {
	return SearchMessage{
		ChatID:      chatID,
		Identity:    identity,
		CreatedAt:   createdAt,
		BodyContent: json.RawMessage(`{"text":"` + testBindingCode + `"}`),
	}
}

func validDiscoveryRecord(t *testing.T, now time.Time, identity string) Record {
	t.Helper()
	codeHash, _, err := canonicalHash(testBindingCode)
	if err != nil {
		t.Fatal(err)
	}
	return Record{
		Version:     recordVersion,
		CodeHash:    codeHash,
		ChannelName: "Team",
		As:          identity,
		CreatedAt:   now,
		ExpiresAt:   now.Add(10 * time.Minute),
	}
}
