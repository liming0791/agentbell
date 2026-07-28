package relay

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	FrameMagic             = "AGBF"
	FrameVersion      byte = 1
	FrameHeaderBytes       = 12
	MaxFrameBodyBytes      = 64 * 1024
)

type FrameKind byte

const (
	FrameKindMetadata FrameKind = 1
	FrameKindEnvelope FrameKind = 2
	FrameKindACK      FrameKind = 3
)

var (
	ErrInvalidFrame     = errors.New("invalid relay stdio frame")
	ErrUnsupportedFrame = errors.New("unsupported relay stdio frame version")
	ErrFrameTooLarge    = errors.New("relay stdio frame body exceeds 64 KiB")
)

// Frame is a single bounded stdio protocol unit. Body is deliberately redacted
// from formatting because it can contain a signed relay envelope or ACK
// identifiers.
type Frame struct {
	Kind FrameKind
	Body []byte
}

func (frame Frame) String() string {
	return fmt.Sprintf(
		"relay.Frame{Kind:%d, Body:<redacted:%d>}",
		frame.Kind,
		len(frame.Body),
	)
}

func (frame Frame) GoString() string {
	return frame.String()
}

func WriteFrame(writer io.Writer, frame Frame) error {
	if writer == nil || !validFrameKind(frame.Kind) || len(frame.Body) == 0 {
		return ErrInvalidFrame
	}
	if len(frame.Body) > MaxFrameBodyBytes {
		return ErrFrameTooLarge
	}
	header := make([]byte, FrameHeaderBytes)
	copy(header[:4], FrameMagic)
	header[4] = FrameVersion
	header[5] = byte(frame.Kind)
	binary.BigEndian.PutUint32(header[8:12], uint32(len(frame.Body)))
	if err := writeFull(writer, header); err != nil {
		return fmt.Errorf("write relay stdio frame header: %w", err)
	}
	if err := writeFull(writer, frame.Body); err != nil {
		return fmt.Errorf("write relay stdio frame body: %w", err)
	}
	return nil
}

func ReadFrame(reader io.Reader) (Frame, error) {
	if reader == nil {
		return Frame{}, ErrInvalidFrame
	}
	header := make([]byte, FrameHeaderBytes)
	if _, err := io.ReadFull(reader, header); err != nil {
		return Frame{}, err
	}
	if string(header[:4]) != FrameMagic ||
		binary.BigEndian.Uint16(header[6:8]) != 0 {
		return Frame{}, ErrInvalidFrame
	}
	if header[4] != FrameVersion {
		return Frame{}, ErrUnsupportedFrame
	}
	kind := FrameKind(header[5])
	if !validFrameKind(kind) {
		return Frame{}, ErrInvalidFrame
	}
	length := binary.BigEndian.Uint32(header[8:12])
	if length == 0 {
		return Frame{}, ErrInvalidFrame
	}
	if length > uint32(MaxFrameBodyBytes) {
		return Frame{}, ErrFrameTooLarge
	}
	body := make([]byte, int(length))
	if _, err := io.ReadFull(reader, body); err != nil {
		if errors.Is(err, io.EOF) {
			return Frame{}, io.ErrUnexpectedEOF
		}
		return Frame{}, err
	}
	return Frame{Kind: kind, Body: body}, nil
}

func validFrameKind(kind FrameKind) bool {
	return kind == FrameKindMetadata ||
		kind == FrameKindEnvelope ||
		kind == FrameKindACK
}

func writeFull(writer io.Writer, value []byte) error {
	for len(value) > 0 {
		written, err := writer.Write(value)
		if err != nil {
			return err
		}
		if written <= 0 || written > len(value) {
			return io.ErrShortWrite
		}
		value = value[written:]
	}
	return nil
}
