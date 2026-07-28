package relay

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

var (
	ErrInvalidForwardRequest = errors.New("invalid relay forward request")
	ErrInvalidForwardACK     = errors.New("invalid durable relay ACK")
	ErrDuplicateACK          = errors.New("duplicate relay ACK")
	ErrStdioClosed           = errors.New("relay stdio transport is closed")
)

const stdioRememberedACKs = 1024

// ForwardRequest contains the exact signed envelope and the authenticated
// metadata needed by any WSL, SSH or container transport. ExactBody is never
// rendered by String or GoString.
type ForwardRequest struct {
	ItemID      string
	DeliveryKey string
	BodyDigest  string
	ExactBody   []byte
	Signature   SignatureMetadata
}

func (request ForwardRequest) String() string {
	return fmt.Sprintf(
		"relay.ForwardRequest{ItemID:<redacted>, DeliveryKey:<redacted>, BodyDigest:<redacted>, ExactBody:<redacted:%d>, Signature:<redacted>}",
		len(request.ExactBody),
	)
}

func (request ForwardRequest) GoString() string {
	return request.String()
}

// ForwardACK is accepted only when it binds a durably committed receiver
// receipt to the exact outbox item that was sent.
type ForwardACK struct {
	ItemID       string    `json:"itemId"`
	DeliveryKey  string    `json:"deliveryKey"`
	BodyDigest   string    `json:"bodyDigest"`
	ReceiptID    string    `json:"receiptId"`
	LocalQueueID string    `json:"localQueueId"`
	Durable      bool      `json:"durable"`
	Duplicate    bool      `json:"duplicate"`
	CommittedAt  time.Time `json:"committedAt"`
}

func (ack ForwardACK) String() string {
	return fmt.Sprintf(
		"relay.ForwardACK{ItemID:<redacted>, DeliveryKey:<redacted>, BodyDigest:<redacted>, ReceiptID:<redacted>, LocalQueueID:<redacted>, Durable:%t, Duplicate:%t, CommittedAt:%s}",
		ack.Durable,
		ack.Duplicate,
		ack.CommittedAt.UTC().Format(time.RFC3339Nano),
	)
}

func (ack ForwardACK) GoString() string {
	return ack.String()
}

func NewForwardRequest(item *OutboxItem) (ForwardRequest, error) {
	if item == nil ||
		item.State != OutboxInflight ||
		item.validatePersisted(OutboxInflight) != nil {
		return ForwardRequest{}, ErrInvalidForwardRequest
	}
	request := ForwardRequest{
		ItemID:      item.ID,
		DeliveryKey: item.DeliveryKey,
		BodyDigest:  item.BodyDigest,
		ExactBody:   bytes.Clone(item.ExactBody),
		Signature:   cloneSignature(item.Signature),
	}
	if err := request.validate(); err != nil {
		return ForwardRequest{}, err
	}
	return request, nil
}

func (request ForwardRequest) ToIngressRequest() (IngressRequest, error) {
	if err := request.validate(); err != nil {
		return IngressRequest{}, err
	}
	return IngressRequest{
		KeyID:     request.Signature.KeyID,
		Method:    request.Signature.Method,
		Target:    request.Signature.Target,
		SentAt:    request.Signature.SentAt,
		Nonce:     request.Signature.Nonce,
		ExactBody: bytes.Clone(request.ExactBody),
		Signature: bytes.Clone(request.Signature.Signature),
	}, nil
}

// NewForwardACK accepts IngressACK specifically because Ingress.Accept creates
// that value only after both the receiver queue and receipt are durably
// committed. It binds that durable result to the exact request on the wire.
func NewForwardACK(
	request ForwardRequest,
	ingressACK IngressACK,
	committedAt time.Time,
) (ForwardACK, error) {
	if request.validate() != nil ||
		ingressACK.ReceiptID != request.ItemID ||
		!validOpaqueWireValue(ingressACK.LocalQueueID, 512) ||
		committedAt.IsZero() {
		return ForwardACK{}, ErrInvalidForwardACK
	}
	ack := ForwardACK{
		ItemID:       request.ItemID,
		DeliveryKey:  request.DeliveryKey,
		BodyDigest:   request.BodyDigest,
		ReceiptID:    ingressACK.ReceiptID,
		LocalQueueID: ingressACK.LocalQueueID,
		Durable:      true,
		Duplicate:    ingressACK.Duplicate,
		CommittedAt:  committedAt.UTC(),
	}
	if err := ack.validateShape(); err != nil {
		return ForwardACK{}, err
	}
	return ack, nil
}

func (request ForwardRequest) validate() error {
	if !validReceiptIdentifier(request.ItemID) ||
		!deliveryPattern.MatchString(request.DeliveryKey) ||
		!deliveryPattern.MatchString(request.BodyDigest) ||
		len(request.ExactBody) == 0 ||
		len(request.ExactBody) > MaxBodyBytes {
		return ErrInvalidForwardRequest
	}
	envelope, err := Decode(request.ExactBody)
	if err != nil ||
		envelope.Delivery.Key != request.DeliveryKey ||
		bodyDigest(request.ExactBody) != request.BodyDigest ||
		receiptID(
			envelope.TeamID,
			envelope.Origin.ID,
			envelope.Delivery.Key,
		) != request.ItemID ||
		request.Signature.Validate(envelope, request.ExactBody) != nil {
		return ErrInvalidForwardRequest
	}
	return nil
}

func (ack ForwardACK) Validate(item *OutboxItem) error {
	if item == nil ||
		item.State != OutboxInflight ||
		ack.validateShape() != nil {
		return ErrInvalidForwardACK
	}
	if ack.ItemID != item.ID ||
		ack.ReceiptID != item.ID ||
		ack.DeliveryKey != item.DeliveryKey ||
		ack.BodyDigest != item.BodyDigest {
		return ErrInvalidForwardACK
	}
	return nil
}

func (ack ForwardACK) validateRequest(request ForwardRequest) error {
	if ack.validateShape() != nil ||
		ack.ItemID != request.ItemID ||
		ack.ReceiptID != request.ItemID ||
		ack.DeliveryKey != request.DeliveryKey ||
		ack.BodyDigest != request.BodyDigest {
		return ErrInvalidForwardACK
	}
	return nil
}

func (ack ForwardACK) validateShape() error {
	if !validReceiptIdentifier(ack.ItemID) ||
		!validReceiptIdentifier(ack.ReceiptID) ||
		!deliveryPattern.MatchString(ack.DeliveryKey) ||
		!deliveryPattern.MatchString(ack.BodyDigest) ||
		!validOpaqueWireValue(ack.LocalQueueID, 512) ||
		!ack.Durable ||
		ack.CommittedAt.IsZero() {
		return ErrInvalidForwardACK
	}
	return nil
}

type forwardMetadata struct {
	ItemID      string    `json:"itemId"`
	DeliveryKey string    `json:"deliveryKey"`
	BodyDigest  string    `json:"bodyDigest"`
	BodyBytes   uint32    `json:"bodyBytes"`
	KeyID       string    `json:"keyId"`
	Method      string    `json:"method"`
	Target      string    `json:"target"`
	SentAt      time.Time `json:"sentAt"`
	Nonce       string    `json:"nonce"`
	Signature   []byte    `json:"signature"`
}

func WriteForwardRequest(writer io.Writer, request ForwardRequest) error {
	if err := request.validate(); err != nil {
		return err
	}
	metadata, err := encodeForwardMetadata(request)
	if err != nil {
		return err
	}
	if err := WriteFrame(writer, Frame{
		Kind: FrameKindMetadata,
		Body: metadata,
	}); err != nil {
		return err
	}
	return WriteFrame(writer, Frame{
		Kind: FrameKindEnvelope,
		Body: request.ExactBody,
	})
}

func ReadForwardRequest(reader io.Reader) (ForwardRequest, error) {
	metadataFrame, err := ReadFrame(reader)
	if err != nil {
		return ForwardRequest{}, err
	}
	if metadataFrame.Kind != FrameKindMetadata {
		return ForwardRequest{}, ErrInvalidForwardRequest
	}
	var metadata forwardMetadata
	if err := strictWireJSON(metadataFrame.Body, &metadata); err != nil ||
		metadata.BodyBytes == 0 ||
		metadata.BodyBytes > MaxBodyBytes {
		return ForwardRequest{}, ErrInvalidForwardRequest
	}
	bodyFrame, err := ReadFrame(reader)
	if err != nil {
		return ForwardRequest{}, err
	}
	if bodyFrame.Kind != FrameKindEnvelope ||
		uint32(len(bodyFrame.Body)) != metadata.BodyBytes {
		return ForwardRequest{}, ErrInvalidForwardRequest
	}
	request := ForwardRequest{
		ItemID:      metadata.ItemID,
		DeliveryKey: metadata.DeliveryKey,
		BodyDigest:  metadata.BodyDigest,
		ExactBody:   bytes.Clone(bodyFrame.Body),
		Signature: SignatureMetadata{
			KeyID:     metadata.KeyID,
			Method:    metadata.Method,
			Target:    metadata.Target,
			SentAt:    metadata.SentAt,
			Nonce:     metadata.Nonce,
			Signature: bytes.Clone(metadata.Signature),
		},
	}
	if err := request.validate(); err != nil {
		return ForwardRequest{}, err
	}
	return request, nil
}

func encodeForwardMetadata(request ForwardRequest) ([]byte, error) {
	if err := request.validate(); err != nil {
		return nil, err
	}
	metadata := forwardMetadata{
		ItemID:      request.ItemID,
		DeliveryKey: request.DeliveryKey,
		BodyDigest:  request.BodyDigest,
		BodyBytes:   uint32(len(request.ExactBody)),
		KeyID:       request.Signature.KeyID,
		Method:      request.Signature.Method,
		Target:      request.Signature.Target,
		SentAt:      request.Signature.SentAt,
		Nonce:       request.Signature.Nonce,
		Signature:   bytes.Clone(request.Signature.Signature),
	}
	encoded, err := json.Marshal(metadata)
	if err != nil || len(encoded) == 0 || len(encoded) > MaxFrameBodyBytes {
		return nil, ErrInvalidForwardRequest
	}
	return encoded, nil
}

func EncodeForwardACK(ack ForwardACK) ([]byte, error) {
	if err := ack.validateShape(); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(ack)
	if err != nil || len(encoded) == 0 || len(encoded) > MaxFrameBodyBytes {
		return nil, ErrInvalidForwardACK
	}
	return encoded, nil
}

func WriteForwardACK(writer io.Writer, ack ForwardACK) error {
	body, err := EncodeForwardACK(ack)
	if err != nil {
		return err
	}
	return WriteFrame(writer, Frame{Kind: FrameKindACK, Body: body})
}

func ReadForwardACK(reader io.Reader) (ForwardACK, error) {
	frame, err := ReadFrame(reader)
	if err != nil {
		return ForwardACK{}, err
	}
	if frame.Kind != FrameKindACK {
		return ForwardACK{}, ErrInvalidForwardACK
	}
	return DecodeForwardACK(frame.Body)
}

func DecodeForwardACK(body []byte) (ForwardACK, error) {
	if len(body) == 0 || len(body) > MaxFrameBodyBytes {
		return ForwardACK{}, ErrInvalidForwardACK
	}
	var wire struct {
		ItemID       string     `json:"itemId"`
		DeliveryKey  string     `json:"deliveryKey"`
		BodyDigest   string     `json:"bodyDigest"`
		ReceiptID    string     `json:"receiptId"`
		LocalQueueID string     `json:"localQueueId"`
		Durable      *bool      `json:"durable"`
		Duplicate    *bool      `json:"duplicate"`
		CommittedAt  *time.Time `json:"committedAt"`
	}
	if err := strictWireJSON(body, &wire); err != nil ||
		wire.Durable == nil ||
		wire.Duplicate == nil ||
		wire.CommittedAt == nil {
		return ForwardACK{}, ErrInvalidForwardACK
	}
	ack := ForwardACK{
		ItemID:       wire.ItemID,
		DeliveryKey:  wire.DeliveryKey,
		BodyDigest:   wire.BodyDigest,
		ReceiptID:    wire.ReceiptID,
		LocalQueueID: wire.LocalQueueID,
		Durable:      *wire.Durable,
		Duplicate:    *wire.Duplicate,
		CommittedAt:  *wire.CommittedAt,
	}
	if ack.validateShape() != nil {
		return ForwardACK{}, ErrInvalidForwardACK
	}
	return ack, nil
}

func strictWireJSON(body []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil {
		return errors.New("invalid relay wire JSON")
	}
	opening, ok := token.(json.Delim)
	if !ok || opening != '{' {
		return errors.New("relay wire JSON must be an object")
	}
	keys := make(map[string]struct{})
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return errors.New("invalid relay wire JSON key")
		}
		key, ok := token.(string)
		if !ok {
			return errors.New("invalid relay wire JSON key")
		}
		if _, duplicate := keys[key]; duplicate {
			return errors.New("duplicate relay wire JSON key")
		}
		keys[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return errors.New("invalid relay wire JSON value")
		}
	}
	if _, err := decoder.Token(); err != nil {
		return errors.New("invalid relay wire JSON object")
	}
	return strictJSON(body, destination)
}

type StdioTransport struct {
	reader io.ReadCloser
	writer io.WriteCloser

	exchangeMutex sync.Mutex
	stateMutex    sync.Mutex
	closeOnce     sync.Once
	closed        bool
	seenACKs      map[string]struct{}
	ackOrder      [stdioRememberedACKs]string
	ackCount      int
	ackCursor     int
}

func NewStdioTransport(
	reader io.ReadCloser,
	writer io.WriteCloser,
) (*StdioTransport, error) {
	if reader == nil || writer == nil {
		return nil, errors.New("relay stdio reader and writer are required")
	}
	return &StdioTransport{
		reader:   reader,
		writer:   writer,
		seenACKs: make(map[string]struct{}),
	}, nil
}

func (transport *StdioTransport) Send(
	ctx context.Context,
	request ForwardRequest,
) (ForwardACK, error) {
	if ctx == nil || request.validate() != nil {
		return ForwardACK{}, ErrInvalidForwardRequest
	}
	request.ExactBody = bytes.Clone(request.ExactBody)
	request.Signature = cloneSignature(request.Signature)
	if err := ctx.Err(); err != nil {
		return ForwardACK{}, err
	}
	transport.exchangeMutex.Lock()
	defer transport.exchangeMutex.Unlock()
	transport.stateMutex.Lock()
	if transport.closed {
		transport.stateMutex.Unlock()
		return ForwardACK{}, ErrStdioClosed
	}
	transport.stateMutex.Unlock()

	type exchangeResult struct {
		ack ForwardACK
		err error
	}
	completed := make(chan exchangeResult, 1)
	go func() {
		if err := WriteForwardRequest(transport.writer, request); err != nil {
			completed <- exchangeResult{err: err}
			return
		}
		ack, err := ReadForwardACK(transport.reader)
		completed <- exchangeResult{ack: ack, err: err}
	}()

	var result exchangeResult
	select {
	case result = <-completed:
	case <-ctx.Done():
		transport.closeStreams()
		<-completed
		return ForwardACK{}, ctx.Err()
	}
	if result.err != nil {
		_ = transport.closeStreams()
		return ForwardACK{}, result.err
	}
	if err := result.ack.validateRequest(request); err != nil {
		_ = transport.closeStreams()
		return ForwardACK{}, err
	}
	if !transport.rememberACK(result.ack.ReceiptID) {
		_ = transport.closeStreams()
		return ForwardACK{}, ErrDuplicateACK
	}
	return result.ack, nil
}

func (transport *StdioTransport) Close() error {
	return transport.closeStreams()
}

func (transport *StdioTransport) closeStreams() error {
	var closeError error
	transport.closeOnce.Do(func() {
		transport.stateMutex.Lock()
		transport.closed = true
		transport.stateMutex.Unlock()
		if err := transport.reader.Close(); err != nil {
			closeError = err
		}
		if err := transport.writer.Close(); err != nil && closeError == nil {
			closeError = err
		}
	})
	return closeError
}

// rememberACK keeps a fixed-size replay window. Receipt binding independently
// rejects an ACK replayed for a different request; the bounded window catches
// duplicates for recently retried requests without unbounded session memory.
func (transport *StdioTransport) rememberACK(receiptID string) bool {
	if _, duplicate := transport.seenACKs[receiptID]; duplicate {
		return false
	}
	if transport.ackCount < len(transport.ackOrder) {
		transport.ackOrder[transport.ackCount] = receiptID
		transport.ackCount++
	} else {
		delete(transport.seenACKs, transport.ackOrder[transport.ackCursor])
		transport.ackOrder[transport.ackCursor] = receiptID
		transport.ackCursor = (transport.ackCursor + 1) % len(transport.ackOrder)
	}
	transport.seenACKs[receiptID] = struct{}{}
	return true
}

func validReceiptIdentifier(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && strings.ToLower(value) == value
}

func validOpaqueWireValue(value string, maximum int) bool {
	if value == "" || len(value) > maximum || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character <= ' ' || character == '\u007f' {
			return false
		}
	}
	return true
}
