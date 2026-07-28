package relay

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/liming0791/agentbell/core/internal/event"
)

type ingressQueue struct {
	calls        int
	notification event.Notification
}

func (queue *ingressQueue) Enqueue(
	notification event.Notification,
	_ time.Time,
) (string, bool, error) {
	queue.calls++
	queue.notification = notification
	return "local-queue-one", queue.calls > 1, nil
}

func TestIngressCommitsQueueReceiptAndReturnsStableACK(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	envelope := validEnvelope(t)
	body := encodedEnvelope(t, envelope)
	signature, err := Sign(
		privateKey,
		"POST",
		"/v1/events",
		envelope.SentAt,
		envelope.Nonce,
		body,
	)
	if err != nil {
		t.Fatal(err)
	}
	nonceStore, err := OpenNonceStore(
		t.TempDir()+"/nonces",
		MinimumNonceRetention,
	)
	if err != nil {
		t.Fatal(err)
	}
	receiptStore, err := OpenReceiptStore(t.TempDir() + "/receipts")
	if err != nil {
		t.Fatal(err)
	}
	queue := &ingressQueue{}
	ingress := Ingress{
		Peer: func(keyID string) (Peer, bool) {
			return Peer{
				ID:              keyID,
				TeamID:          envelope.TeamID,
				OriginID:        envelope.Origin.ID,
				PublicKey:       publicKey,
				Scopes:          []string{ScopeIngest},
				AllowedSources:  []string{envelope.Event.Source},
				AllowedRuntimes: []string{envelope.Event.Runtime},
			}, keyID == "peer-one"
		},
		Nonces:   nonceStore,
		Receipts: receiptStore,
		Queue:    queue,
		Now:      func() time.Time { return envelope.SentAt.Add(time.Minute) },
	}
	request := IngressRequest{
		KeyID:     "peer-one",
		Method:    "POST",
		Target:    "/v1/events",
		SentAt:    envelope.SentAt,
		Nonce:     envelope.Nonce,
		ExactBody: body,
		Signature: signature,
	}
	first, err := ingress.Accept(request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Duplicate || first.LocalQueueID != "local-queue-one" ||
		first.ReceiptID == "" || queue.calls != 1 {
		t.Fatalf("first ACK = %#v calls=%d", first, queue.calls)
	}
	if queue.notification.IdempotencyKey != envelope.Delivery.Key {
		t.Fatalf(
			"local key = %q, want origin-scoped %q",
			queue.notification.IdempotencyKey,
			envelope.Delivery.Key,
		)
	}

	// An ACK-lost retry is the byte-identical authenticated request. It returns
	// the durable receipt without consuming the queue or weakening nonce
	// protection for any different request.
	second, err := ingress.Accept(request)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Duplicate || second.ReceiptID != first.ReceiptID ||
		second.LocalQueueID != first.LocalQueueID || queue.calls != 1 {
		t.Fatalf("retry ACK = %#v calls=%d", second, queue.calls)
	}
}

func TestIngressRejectsNonceReplayBeforeQueueMutation(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	firstEnvelope := validEnvelope(t)
	nonces, err := OpenNonceStore(t.TempDir()+"/nonces", MinimumNonceRetention)
	if err != nil {
		t.Fatal(err)
	}
	if accepted, err := nonces.Accept(
		"peer-one",
		firstEnvelope.Nonce,
		firstEnvelope.SentAt,
	); err != nil || !accepted {
		t.Fatalf("reserve nonce: accepted=%v err=%v", accepted, err)
	}

	secondEnvelope := firstEnvelope
	secondEnvelope.Delivery.ProducerKey = "sha256:" + strings.Repeat("c", 64)
	secondEnvelope.Event.IdempotencyKey = secondEnvelope.Delivery.ProducerKey
	secondEnvelope.Delivery.Key, err = DeriveDeliveryKey(
		secondEnvelope.TeamID,
		secondEnvelope.Origin.ID,
		secondEnvelope.Delivery.ProducerKey,
	)
	if err != nil {
		t.Fatal(err)
	}
	secondBody := encodedEnvelope(t, secondEnvelope)
	secondSignature, err := Sign(
		privateKey,
		"POST",
		"/v1/events",
		secondEnvelope.SentAt,
		secondEnvelope.Nonce,
		secondBody,
	)
	if err != nil {
		t.Fatal(err)
	}
	receipts, err := OpenReceiptStore(t.TempDir() + "/receipts")
	if err != nil {
		t.Fatal(err)
	}
	queue := &ingressQueue{}
	ingress := Ingress{
		Peer: func(string) (Peer, bool) {
			return Peer{
				ID:              "peer-one",
				TeamID:          secondEnvelope.TeamID,
				OriginID:        secondEnvelope.Origin.ID,
				PublicKey:       publicKey,
				Scopes:          []string{ScopeIngest},
				AllowedSources:  []string{secondEnvelope.Event.Source},
				AllowedRuntimes: []string{secondEnvelope.Event.Runtime},
			}, true
		},
		Nonces: nonces, Receipts: receipts, Queue: queue,
		Now: func() time.Time { return secondEnvelope.SentAt },
	}
	_, err = ingress.Accept(IngressRequest{
		KeyID:     "peer-one",
		Method:    "POST",
		Target:    "/v1/events",
		SentAt:    secondEnvelope.SentAt,
		Nonce:     secondEnvelope.Nonce,
		ExactBody: secondBody,
		Signature: secondSignature,
	})
	if !errors.Is(err, ErrNonceReplay) || queue.calls != 0 {
		t.Fatalf("replay error=%v queue calls=%d", err, queue.calls)
	}
}
