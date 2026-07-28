package relay

import (
	"errors"
	"strings"
	"time"

	"github.com/liming0791/agentbell/core/internal/event"
)

var (
	ErrUnknownPeer = errors.New("unknown relay peer")
	ErrNonceReplay = errors.New("relay nonce was already used")
)

type PeerLookup func(keyID string) (Peer, bool)

type EventQueue interface {
	Enqueue(event.Notification, time.Time) (string, bool, error)
}

type Ingress struct {
	Peer     PeerLookup
	Nonces   *NonceStore
	Receipts *ReceiptStore
	Queue    EventQueue
	Now      func() time.Time
	MaxSkew  time.Duration
}

type IngressRequest struct {
	KeyID     string
	Method    string
	Target    string
	SentAt    time.Time
	Nonce     string
	ExactBody []byte
	Signature []byte
}

type IngressACK struct {
	ReceiptID    string `json:"receiptId"`
	LocalQueueID string `json:"localQueueId"`
	Duplicate    bool   `json:"duplicate"`
}

func (ingress Ingress) Accept(request IngressRequest) (IngressACK, error) {
	if ingress.Peer == nil ||
		ingress.Nonces == nil ||
		ingress.Receipts == nil ||
		ingress.Queue == nil {
		return IngressACK{}, errors.New("relay ingress dependencies are incomplete")
	}
	if strings.TrimSpace(request.KeyID) == "" ||
		request.SentAt.IsZero() ||
		request.Nonce == "" {
		return IngressACK{}, errors.New("relay ingress request is incomplete")
	}
	peer, ok := ingress.Peer(request.KeyID)
	if !ok || peer.ID != request.KeyID {
		return IngressACK{}, ErrUnknownPeer
	}
	now := time.Now().UTC()
	if ingress.Now != nil {
		now = ingress.Now().UTC()
	}
	envelope, err := VerifyPeer(
		peer,
		ScopeIngest,
		request.Method,
		request.Target,
		request.ExactBody,
		request.Signature,
		now,
		ingress.MaxSkew,
	)
	if err != nil {
		return IngressACK{}, err
	}
	if !request.SentAt.Equal(envelope.SentAt) ||
		request.Nonce != envelope.Nonce {
		return IngressACK{}, errors.New(
			"relay signature headers do not match the envelope",
		)
	}

	if existing, found, lookupErr := ingress.Receipts.LookupCommitted(
		request.ExactBody,
	); lookupErr != nil {
		return IngressACK{}, lookupErr
	} else if found {
		return ingressACK(existing, true)
	}

	accepted, err := ingress.Nonces.Accept(peer.ID, request.Nonce, now)
	if err != nil {
		return IngressACK{}, err
	}
	if !accepted {
		// Resolve the race where an identical request reserved the nonce and
		// committed its receipt between our first lookup and nonce reservation.
		if existing, found, lookupErr := ingress.Receipts.LookupCommitted(
			request.ExactBody,
		); lookupErr != nil {
			return IngressACK{}, lookupErr
		} else if found {
			return ingressACK(existing, true)
		}
		return IngressACK{}, ErrNonceReplay
	}

	receipt, duplicate, err := ingress.Receipts.CommitIngress(
		request.ExactBody,
		now,
		func(value Envelope, _ []byte) (string, error) {
			notification := value.Event
			notification.IdempotencyKey = value.Delivery.Key
			id, _, enqueueErr := ingress.Queue.Enqueue(notification, now)
			return id, enqueueErr
		},
	)
	if err != nil {
		return IngressACK{}, err
	}
	return ingressACK(receipt, duplicate)
}

func ingressACK(receipt Receipt, duplicate bool) (IngressACK, error) {
	if !receipt.ACKEligible() {
		return IngressACK{}, errors.New(
			"relay receipt is not durably committed",
		)
	}
	return IngressACK{
		ReceiptID:    receipt.ID,
		LocalQueueID: receipt.LocalQueueID,
		Duplicate:    duplicate,
	}, nil
}
