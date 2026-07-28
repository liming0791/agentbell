package relay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestForwardRequestFramesPreserveExactBodyAndMetadata(t *testing.T) {
	item := claimedOutboxItem(t)
	request, err := NewForwardRequest(item)
	if err != nil {
		t.Fatal(err)
	}
	var wire bytes.Buffer
	if err := WriteForwardRequest(&wire, request); err != nil {
		t.Fatal(err)
	}
	decoded, err := ReadForwardRequest(&wire)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ItemID != item.ID ||
		decoded.DeliveryKey != item.DeliveryKey ||
		decoded.BodyDigest != item.BodyDigest ||
		!bytes.Equal(decoded.ExactBody, item.ExactBody) ||
		!bytes.Equal(decoded.Signature.Signature, item.Signature.Signature) {
		t.Fatalf("request changed across framing: %#v", decoded)
	}
	if strings.Contains(request.String(), string(item.ExactBody)) ||
		strings.Contains(decoded.GoString(), string(item.ExactBody)) {
		t.Fatal("request formatting leaked exact body")
	}
	ingressRequest, err := decoded.ToIngressRequest()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(ingressRequest.ExactBody, item.ExactBody) ||
		!bytes.Equal(ingressRequest.Signature, item.Signature.Signature) ||
		ingressRequest.KeyID != item.Signature.KeyID {
		t.Fatalf("ingress request changed: %#v", ingressRequest)
	}
}

func TestReadForwardRequestRejectsWrongOrderAndBodyIntegrity(t *testing.T) {
	item := claimedOutboxItem(t)
	request, err := NewForwardRequest(item)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		build func(*bytes.Buffer)
	}{
		{
			"wrong first kind",
			func(wire *bytes.Buffer) {
				mustWriteFrame(t, wire, Frame{
					Kind: FrameKindEnvelope,
					Body: request.ExactBody,
				})
			},
		},
		{
			"wrong second kind",
			func(wire *bytes.Buffer) {
				metadata, marshalErr := encodeForwardMetadata(request)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				mustWriteFrame(t, wire, Frame{Kind: FrameKindMetadata, Body: metadata})
				mustWriteFrame(t, wire, Frame{Kind: FrameKindACK, Body: []byte(`{}`)})
			},
		},
		{
			"body mismatch",
			func(wire *bytes.Buffer) {
				metadata, marshalErr := encodeForwardMetadata(request)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				mustWriteFrame(t, wire, Frame{Kind: FrameKindMetadata, Body: metadata})
				mustWriteFrame(t, wire, Frame{
					Kind: FrameKindEnvelope,
					Body: append(bytes.Clone(request.ExactBody), ' '),
				})
			},
		},
		{
			"unknown metadata",
			func(wire *bytes.Buffer) {
				mustWriteFrame(t, wire, Frame{
					Kind: FrameKindMetadata,
					Body: []byte(`{"unknown":true}`),
				})
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var wire bytes.Buffer
			test.build(&wire)
			if _, readErr := ReadForwardRequest(&wire); readErr == nil {
				t.Fatal("invalid request frames were accepted")
			}
		})
	}
}

func TestACKStrictJSONAndDurableBinding(t *testing.T) {
	item := claimedOutboxItem(t)
	request, err := NewForwardRequest(item)
	if err != nil {
		t.Fatal(err)
	}
	ack, err := NewForwardACK(request, IngressACK{
		ReceiptID:    item.ID,
		LocalQueueID: "local-queue-item",
		Duplicate:    true,
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if !ack.Durable || !ack.Duplicate {
		t.Fatalf("durable ingress ACK was not preserved: %#v", ack)
	}
	encoded, err := EncodeForwardACK(ack)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeForwardACK(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if err := decoded.Validate(item); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(decoded.String(), item.ID) ||
		strings.Contains(decoded.GoString(), item.DeliveryKey) {
		t.Fatal("ACK formatting leaked relay identifiers")
	}
	var ackWire bytes.Buffer
	if err := WriteForwardACK(&ackWire, ack); err != nil {
		t.Fatal(err)
	}
	framedACK, err := ReadForwardACK(&ackWire)
	if err != nil || framedACK.ReceiptID != ack.ReceiptID {
		t.Fatalf("framed ACK = %#v err=%v", framedACK, err)
	}

	for _, invalid := range [][]byte{
		append(bytes.Clone(encoded), []byte(` {}`)...),
		[]byte(`{"unknown":true}`),
		[]byte(`{"itemId":`),
		bytes.Replace(encoded, []byte(`,"duplicate":true`), nil, 1),
		append(
			[]byte(`{"itemId":"`+item.ID+`","itemId":"`+item.ID+`",`),
			encoded[1+len(`"itemId":"`+item.ID+`",`):]...,
		),
	} {
		if _, err := DecodeForwardACK(invalid); err == nil {
			t.Fatalf("invalid ACK accepted: %q", invalid)
		}
	}
	tests := []struct {
		name   string
		mutate func(*ForwardACK)
	}{
		{"not durable", func(value *ForwardACK) { value.Durable = false }},
		{"missing committed time", func(value *ForwardACK) { value.CommittedAt = time.Time{} }},
		{"wrong item", func(value *ForwardACK) { value.ItemID = "wrong" }},
		{"wrong receipt", func(value *ForwardACK) { value.ReceiptID = "wrong" }},
		{"wrong delivery", func(value *ForwardACK) { value.DeliveryKey = "wrong" }},
		{"wrong digest", func(value *ForwardACK) { value.BodyDigest = "wrong" }},
		{"missing local queue", func(value *ForwardACK) { value.LocalQueueID = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := ack
			test.mutate(&candidate)
			if err := candidate.Validate(item); err == nil {
				t.Fatal("invalid ACK was accepted")
			}
		})
	}
}

func TestStdioTransportExchangeAndDuplicateACK(t *testing.T) {
	item := claimedOutboxItem(t)
	request, err := NewForwardRequest(item)
	if err != nil {
		t.Fatal(err)
	}
	ack := validForwardACK(item)
	ackFrame := encodedACKFrame(t, ack)
	reader := &closeBuffer{Reader: bytes.NewReader(append(
		bytes.Clone(ackFrame),
		ackFrame...,
	))}
	writer := &closeBuffer{}
	transport, err := NewStdioTransport(reader, writer)
	if err != nil {
		t.Fatal(err)
	}

	got, err := transport.Send(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := got.Validate(item); err != nil {
		t.Fatal(err)
	}
	decodedRequest, err := ReadForwardRequest(bytes.NewReader(writer.Bytes()))
	if err != nil {
		t.Fatalf("written request: %v", err)
	}
	if !bytes.Equal(decodedRequest.ExactBody, item.ExactBody) {
		t.Fatal("stdio transport changed exact body")
	}

	if _, err := transport.Send(context.Background(), request); !errors.Is(err, ErrDuplicateACK) {
		t.Fatalf("duplicate ACK error = %v", err)
	}
}

func TestStdioTransportRejectsWrongReceiptAndKind(t *testing.T) {
	item := claimedOutboxItem(t)
	request, err := NewForwardRequest(item)
	if err != nil {
		t.Fatal(err)
	}
	wrong := validForwardACK(item)
	wrong.ReceiptID = strings.Repeat("0", 64)
	tests := []struct {
		name string
		wire []byte
	}{
		{"wrong receipt", encodedACKFrame(t, wrong)},
		{
			"wrong kind",
			func() []byte {
				var wire bytes.Buffer
				mustWriteFrame(t, &wire, Frame{
					Kind: FrameKindEnvelope,
					Body: []byte("not an ack"),
				})
				return wire.Bytes()
			}(),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport, newErr := NewStdioTransport(
				&closeBuffer{Reader: bytes.NewReader(test.wire)},
				&closeBuffer{},
			)
			if newErr != nil {
				t.Fatal(newErr)
			}
			if _, sendErr := transport.Send(context.Background(), request); sendErr == nil {
				t.Fatal("invalid ACK was accepted")
			}
		})
	}
}

func TestStdioTransportContextCancellationClosesBlockedStream(t *testing.T) {
	reader, peerWriter := io.Pipe()
	defer peerWriter.Close()
	writer := &closeBuffer{}
	transport, err := NewStdioTransport(reader, writer)
	if err != nil {
		t.Fatal(err)
	}
	item := claimedOutboxItem(t)
	request, err := NewForwardRequest(item)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err = transport.Send(ctx, request)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation error = %v", err)
	}
	if !writer.isClosed() {
		t.Fatal("cancellation did not close stdio writer")
	}
}

func TestStdioTransportCloseInterruptsBlockedExchange(t *testing.T) {
	reader, peerWriter := io.Pipe()
	defer peerWriter.Close()
	writer := &closeBuffer{writeSignal: make(chan struct{})}
	transport, err := NewStdioTransport(reader, writer)
	if err != nil {
		t.Fatal(err)
	}
	item := claimedOutboxItem(t)
	request, err := NewForwardRequest(item)
	if err != nil {
		t.Fatal(err)
	}
	completed := make(chan error, 1)
	go func() {
		_, sendErr := transport.Send(context.Background(), request)
		completed <- sendErr
	}()
	select {
	case <-writer.writeSignal:
	case <-time.After(time.Second):
		t.Fatal("exchange did not start writing")
	}
	if err := transport.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case sendErr := <-completed:
		if sendErr == nil {
			t.Fatal("closed exchange returned success")
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not interrupt blocked ACK read")
	}
}

func TestStdioTransportCleanEOFAndConstructorValidation(t *testing.T) {
	if _, err := NewStdioTransport(nil, &closeBuffer{}); err == nil {
		t.Fatal("nil reader accepted")
	}
	if _, err := NewStdioTransport(&closeBuffer{}, nil); err == nil {
		t.Fatal("nil writer accepted")
	}
	item := claimedOutboxItem(t)
	request, err := NewForwardRequest(item)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := NewStdioTransport(
		&closeBuffer{Reader: bytes.NewReader(nil)},
		&closeBuffer{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.Send(context.Background(), request); !errors.Is(err, io.EOF) {
		t.Fatalf("clean EOF = %v", err)
	}
}

func TestStdioACKReplayWindowIsBounded(t *testing.T) {
	transport := &StdioTransport{
		seenACKs: make(map[string]struct{}),
	}
	first := fmt.Sprintf("%064x", 0)
	for index := 0; index <= stdioRememberedACKs; index++ {
		if !transport.rememberACK(fmt.Sprintf("%064x", index)) {
			t.Fatalf("new ACK %d was treated as duplicate", index)
		}
	}
	if len(transport.seenACKs) != stdioRememberedACKs {
		t.Fatalf("remembered ACKs = %d", len(transport.seenACKs))
	}
	if !transport.rememberACK(first) {
		t.Fatal("oldest ACK was not evicted from bounded replay window")
	}
}

func mustWriteFrame(t *testing.T, writer io.Writer, frame Frame) {
	t.Helper()
	if err := WriteFrame(writer, frame); err != nil {
		t.Fatal(err)
	}
}

func encodedACKFrame(t *testing.T, ack ForwardACK) []byte {
	t.Helper()
	var wire bytes.Buffer
	if err := WriteForwardACK(&wire, ack); err != nil {
		t.Fatal(err)
	}
	return wire.Bytes()
}

type closeBuffer struct {
	sync.Mutex
	*bytes.Reader
	buffer      bytes.Buffer
	closed      bool
	writeSignal chan struct{}
	writeOnce   sync.Once
}

func (buffer *closeBuffer) Read(value []byte) (int, error) {
	buffer.Lock()
	defer buffer.Unlock()
	if buffer.closed {
		return 0, io.ErrClosedPipe
	}
	if buffer.Reader == nil {
		return 0, io.EOF
	}
	return buffer.Reader.Read(value)
}

func (buffer *closeBuffer) Write(value []byte) (int, error) {
	buffer.Lock()
	defer buffer.Unlock()
	if buffer.closed {
		return 0, io.ErrClosedPipe
	}
	buffer.writeOnce.Do(func() {
		if buffer.writeSignal != nil {
			close(buffer.writeSignal)
		}
	})
	return buffer.buffer.Write(value)
}

func (buffer *closeBuffer) Close() error {
	buffer.Lock()
	defer buffer.Unlock()
	buffer.closed = true
	return nil
}

func (buffer *closeBuffer) Bytes() []byte {
	buffer.Lock()
	defer buffer.Unlock()
	return bytes.Clone(buffer.buffer.Bytes())
}

func (buffer *closeBuffer) isClosed() bool {
	buffer.Lock()
	defer buffer.Unlock()
	return buffer.closed
}
