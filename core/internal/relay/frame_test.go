package relay

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestFrameRoundTripUsesFixedHeaderAndBoundedBody(t *testing.T) {
	body := []byte(`{"ok":true}`)
	var wire bytes.Buffer
	if err := WriteFrame(&wire, Frame{Kind: FrameKindACK, Body: body}); err != nil {
		t.Fatal(err)
	}
	encoded := wire.Bytes()
	if len(encoded) != FrameHeaderBytes+len(body) {
		t.Fatalf("wire length = %d", len(encoded))
	}
	if string(encoded[:4]) != FrameMagic ||
		encoded[4] != FrameVersion ||
		encoded[5] != byte(FrameKindACK) ||
		binary.BigEndian.Uint16(encoded[6:8]) != 0 ||
		binary.BigEndian.Uint32(encoded[8:12]) != uint32(len(body)) {
		t.Fatalf("unexpected frame header: %x", encoded[:FrameHeaderBytes])
	}

	frame, err := ReadFrame(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if frame.Kind != FrameKindACK || !bytes.Equal(frame.Body, body) {
		t.Fatalf("frame = %#v", frame)
	}
	if strings.Contains(frame.String(), string(body)) ||
		strings.Contains(frame.GoString(), string(body)) {
		t.Fatalf("frame formatting leaked body: %s", frame)
	}
}

func TestReadFrameRejectsHeaderBeforeAllocatingBody(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]byte)
		want   error
	}{
		{
			"magic",
			func(header []byte) { copy(header[:4], "NOPE") },
			ErrInvalidFrame,
		},
		{
			"version",
			func(header []byte) { header[4] = FrameVersion + 1 },
			ErrUnsupportedFrame,
		},
		{
			"kind",
			func(header []byte) { header[5] = 0xff },
			ErrInvalidFrame,
		},
		{
			"reserved",
			func(header []byte) { binary.BigEndian.PutUint16(header[6:8], 1) },
			ErrInvalidFrame,
		},
		{
			"empty",
			func(header []byte) { binary.BigEndian.PutUint32(header[8:12], 0) },
			ErrInvalidFrame,
		},
		{
			"oversized",
			func(header []byte) {
				binary.BigEndian.PutUint32(
					header[8:12],
					uint32(MaxFrameBodyBytes+1),
				)
			},
			ErrFrameTooLarge,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			header := validFrameHeader(FrameKindACK, 1)
			test.mutate(header)
			reader := &countingReader{reader: bytes.NewReader(header)}
			_, err := ReadFrame(reader)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v", err)
			}
			if reader.bytesRead != FrameHeaderBytes {
				t.Fatalf("read %d bytes before rejecting header", reader.bytesRead)
			}
		})
	}

	if _, err := ReadFrame(bytes.NewReader(nil)); !errors.Is(err, io.EOF) {
		t.Fatalf("clean EOF = %v", err)
	}
	if _, err := ReadFrame(bytes.NewReader([]byte(FrameMagic))); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("partial header = %v", err)
	}
	header := validFrameHeader(FrameKindACK, 4)
	if _, err := ReadFrame(bytes.NewReader(header)); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("partial body = %v", err)
	}
}

func TestWriteFrameRejectsInvalidOrOversizedBody(t *testing.T) {
	tests := []Frame{
		{},
		{Kind: FrameKind(0xff), Body: []byte("x")},
		{Kind: FrameKindACK, Body: make([]byte, MaxFrameBodyBytes+1)},
	}
	for _, frame := range tests {
		var output bytes.Buffer
		if err := WriteFrame(&output, frame); err == nil {
			t.Fatalf("invalid frame was written: %#v", frame)
		}
		if output.Len() != 0 {
			t.Fatalf("partial invalid frame output: %d", output.Len())
		}
	}
	if err := WriteFrame(failingWriter{}, Frame{
		Kind: FrameKindACK,
		Body: []byte("x"),
	}); err == nil {
		t.Fatal("writer failure was ignored")
	}

	maximum := bytes.Repeat([]byte{'x'}, MaxFrameBodyBytes)
	var wire bytes.Buffer
	if err := WriteFrame(&wire, Frame{
		Kind: FrameKindEnvelope,
		Body: maximum,
	}); err != nil {
		t.Fatalf("maximum body: %v", err)
	}
	decoded, err := ReadFrame(&wire)
	if err != nil || len(decoded.Body) != MaxFrameBodyBytes {
		t.Fatalf("maximum frame body=%d err=%v", len(decoded.Body), err)
	}
}

func validFrameHeader(kind FrameKind, length uint32) []byte {
	header := make([]byte, FrameHeaderBytes)
	copy(header[:4], FrameMagic)
	header[4] = FrameVersion
	header[5] = byte(kind)
	binary.BigEndian.PutUint32(header[8:12], length)
	return header
}

type countingReader struct {
	reader    io.Reader
	bytesRead int
}

func (reader *countingReader) Read(value []byte) (int, error) {
	count, err := reader.reader.Read(value)
	reader.bytesRead += count
	return count, err
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}
