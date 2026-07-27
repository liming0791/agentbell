package relay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const defaultRelayStressItems = 24

type relayStressInput struct {
	body      []byte
	signature SignatureMetadata
}

type relayStressTransport struct {
	expected map[string][]byte
	attempts map[string]int
	received map[string]bool
	secret   string
	lastID   string
}

func (transport *relayStressTransport) Send(
	_ context.Context,
	request ForwardRequest,
) (ForwardACK, error) {
	expected, ok := transport.expected[request.ItemID]
	if !ok || !bytes.Equal(expected, request.ExactBody) {
		return ForwardACK{}, errors.New("stress body mismatch")
	}
	transport.attempts[request.ItemID]++
	transport.received[request.ItemID] = true
	transport.lastID = request.ItemID
	switch transport.attempts[request.ItemID] {
	case 1:
		return ForwardACK{}, errors.New(transport.secret)
	case 2:
		// The receiver durably committed this delivery but the ACK was lost.
		// Its receipt key deduplicates the repeated exact request.
		return ForwardACK{}, io.EOF
	default:
		return validForwardACK(&OutboxItem{
			ID:          request.ItemID,
			DeliveryKey: request.DeliveryKey,
			BodyDigest:  request.BodyDigest,
		}), nil
	}
}

func TestRelayDurableStressGate(t *testing.T) {
	total := relayStressItemCount(t)
	inputs := relayStressInputs(t, total)
	root := filepath.Join(t.TempDir(), "durable-outbox")
	outbox, err := OpenOutbox(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	expected := make(map[string][]byte, total)
	for index, input := range inputs {
		id, duplicate, enqueueErr := outbox.EnqueueBounded(
			input.body,
			input.signature,
			now.Add(time.Duration(index)*time.Millisecond),
			64<<20,
		)
		if enqueueErr != nil || duplicate {
			t.Fatalf(
				"enqueue %d: id=%q duplicate=%v err=%v",
				index,
				id,
				duplicate,
				enqueueErr,
			)
		}
		expected[id] = bytes.Clone(input.body)
	}

	// Simulate abrupt process death after claims were persisted but before any
	// transport call or NACK.
	crashed := total / 4
	if crashed < 1 {
		crashed = 1
	}
	for index := 0; index < crashed; index++ {
		item, claimErr := outbox.Claim(now, time.Millisecond)
		if claimErr != nil || item == nil {
			t.Fatalf("crash claim %d: item=%#v err=%v", index, item, claimErr)
		}
	}
	outbox, err = OpenOutbox(root)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := outbox.Recover(now.Add(2 * time.Millisecond))
	if err != nil || recovered != crashed {
		t.Fatalf("recovered=%d want=%d err=%v", recovered, crashed, err)
	}

	secret := "https://user:token@private.example/internal"
	transport := &relayStressTransport{
		expected: expected,
		attempts: map[string]int{},
		received: map[string]bool{},
		secret:   secret,
	}
	current := now.Add(3 * time.Millisecond)
	forwarder := Forwarder{
		Outbox:    outbox,
		Transport: transport,
		Now:       func() time.Time { return current },
		Lease:     time.Second,
		Backoff: []time.Duration{
			time.Millisecond,
			time.Millisecond,
			time.Millisecond,
			time.Millisecond,
		},
	}
	maxIterations := total * 4
	for iteration := 0; iteration < maxIterations; iteration++ {
		stats, statsErr := outbox.Stats()
		if statsErr != nil {
			t.Fatal(statsErr)
		}
		if stats.History == total {
			break
		}
		forwarded, forwardErr := forwarder.ForwardOne(
			context.Background(),
		)
		if !forwarded && forwardErr == nil {
			current = current.Add(2 * time.Millisecond)
			continue
		}
		if forwardErr != nil &&
			strings.Contains(forwardErr.Error(), secret) {
			t.Fatalf("forward error leaked transport detail: %v", forwardErr)
		}
		if forwardErr != nil {
			pending, readErr := outbox.read(
				OutboxPending,
				transport.lastID,
			)
			if readErr != nil {
				t.Fatalf("failed retry was not durable: %v", readErr)
			}
			if strings.Contains(pending.LastError, secret) ||
				len(pending.LastError) > maxPersistedErrorBytes {
				t.Fatalf(
					"persisted retry error was unsafe: %q",
					pending.LastError,
				)
			}
		}
		current = current.Add(2 * time.Millisecond)
	}
	stats, err := outbox.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.History != total ||
		stats.Pending != 0 ||
		stats.Inflight != 0 ||
		stats.Dead != 0 ||
		len(transport.received) != total {
		t.Fatalf(
			"durability invariant failed: stats=%#v received=%d",
			stats,
			len(transport.received),
		)
	}
	for id, attempts := range transport.attempts {
		if attempts != 3 {
			t.Fatalf("item %s attempts=%d want=3", id, attempts)
		}
		history, readErr := outbox.read(OutboxHistory, id)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !bytes.Equal(history.ExactBody, expected[id]) ||
			history.LastError != "" {
			t.Fatalf("history item %s changed or retained an error", id)
		}
	}

	stressCapacityBound(t, inputs, now)
	t.Logf(
		"M2_STRESS_PASS items=%d deliveries=%d attempts=%d recovered=%d",
		total,
		len(transport.received),
		total*3,
		recovered,
	)
}

func stressCapacityBound(
	t *testing.T,
	inputs []relayStressInput,
	now time.Time,
) {
	t.Helper()
	outbox, err := OpenOutbox(filepath.Join(t.TempDir(), "bounded-outbox"))
	if err != nil {
		t.Fatal(err)
	}
	firstID, duplicate, err := outbox.EnqueueBounded(
		inputs[0].body,
		inputs[0].signature,
		now,
		64<<20,
	)
	if err != nil || duplicate {
		t.Fatalf("capacity seed: id=%q duplicate=%v err=%v", firstID, duplicate, err)
	}
	firstUsage, err := outbox.retryUsage()
	if err != nil {
		t.Fatal(err)
	}
	limit := firstUsage * 4
	var accepted atomic.Int64
	accepted.Store(1)
	var capacity atomic.Int64
	var unexpected atomic.Int64
	var wait sync.WaitGroup
	for index := 1; index < len(inputs); index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, duplicate, enqueueErr := outbox.EnqueueBounded(
				inputs[index].body,
				inputs[index].signature,
				now.Add(time.Duration(index)*time.Millisecond),
				limit,
			)
			switch {
			case enqueueErr == nil && !duplicate:
				accepted.Add(1)
			case errors.Is(enqueueErr, ErrOutboxCapacity):
				capacity.Add(1)
			default:
				unexpected.Add(1)
			}
		}()
	}
	wait.Wait()
	usage, err := outbox.retryUsage()
	if err != nil {
		t.Fatal(err)
	}
	stats, err := outbox.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if unexpected.Load() != 0 ||
		capacity.Load() == 0 ||
		usage > limit ||
		int64(stats.Pending) != accepted.Load() {
		t.Fatalf(
			"capacity invariant: accepted=%d capacity=%d unexpected=%d usage=%d limit=%d stats=%#v",
			accepted.Load(),
			capacity.Load(),
			unexpected.Load(),
			usage,
			limit,
			stats,
		)
	}
	duplicateID, wasDuplicate, err := outbox.EnqueueBounded(
		inputs[0].body,
		inputs[0].signature,
		now.Add(time.Hour),
		1,
	)
	if err != nil || !wasDuplicate || duplicateID != firstID {
		t.Fatalf(
			"full outbox rejected idempotent duplicate: id=%q duplicate=%v err=%v",
			duplicateID,
			wasDuplicate,
			err,
		)
	}
}

func relayStressItemCount(t *testing.T) int {
	t.Helper()
	raw := os.Getenv("AGENTBELL_M2_STRESS_ITEMS")
	if raw == "" {
		return defaultRelayStressItems
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 8 || value > 512 {
		t.Fatalf("AGENTBELL_M2_STRESS_ITEMS must be between 8 and 512")
	}
	return value
}

func relayStressInputs(t *testing.T, total int) []relayStressInput {
	t.Helper()
	seed := sha256.Sum256([]byte("agentbell-m2-relay-stress-seed"))
	privateKey := ed25519.NewKeyFromSeed(seed[:])
	inputs := make([]relayStressInput, 0, total)
	base := time.Date(2026, 7, 27, 11, 0, 0, 0, time.UTC)
	for index := 0; index < total; index++ {
		envelope := validEnvelope(t)
		producerDigest := sha256.Sum256([]byte(fmt.Sprintf(
			"stress-producer-%06d",
			index,
		)))
		envelope.Event.IdempotencyKey =
			"sha256:" + hex.EncodeToString(producerDigest[:])
		envelope.Delivery.ProducerKey = envelope.Event.IdempotencyKey
		key, err := DeriveDeliveryKey(
			envelope.TeamID,
			envelope.Origin.ID,
			envelope.Delivery.ProducerKey,
		)
		if err != nil {
			t.Fatal(err)
		}
		envelope.Delivery.Key = key
		envelope.SentAt = base.Add(time.Duration(index) * time.Millisecond)
		envelope.Event.OccurredAt = envelope.SentAt.Add(-time.Second)
		nonceDigest := sha256.Sum256([]byte(fmt.Sprintf(
			"stress-nonce-%06d",
			index,
		)))
		envelope.Nonce = hex.EncodeToString(nonceDigest[:16])
		body := encodedEnvelope(t, envelope)
		signature, err := Sign(
			privateKey,
			"POST",
			"/v1/relay/events",
			envelope.SentAt,
			envelope.Nonce,
			body,
		)
		if err != nil {
			t.Fatal(err)
		}
		inputs = append(inputs, relayStressInput{
			body: body,
			signature: SignatureMetadata{
				KeyID:     "stress-key",
				Method:    "POST",
				Target:    "/v1/relay/events",
				SentAt:    envelope.SentAt,
				Nonce:     envelope.Nonce,
				Signature: signature,
			},
		})
	}
	return inputs
}
