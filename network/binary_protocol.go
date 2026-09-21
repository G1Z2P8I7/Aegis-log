package network

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
)

// MagicByte identifies valid KhorosLog wire frames ('K' = 0x4B).
const MagicByte byte = 0x4B

// HeaderSize is the fixed 17-byte layout:
// [Magic (1B)] + [Length (4B)] + [Offset (8B)] + [CRC32 (4B)]
const HeaderSize = 1 + 4 + 8 + 4

// MaxPayloadSize sets a sanity boundary (32MB) to prevent OOM on malformed TCP streams.
const MaxPayloadSize uint32 = 32 * 1024 * 1024

// CommandOp defines high-throughput TCP client commands.
type CommandOp byte

const (
	CmdProduce      CommandOp = 0x01
	CmdConsume      CommandOp = 0x02
	CmdCommitOffset CommandOp = 0x03
	CmdFetchOffset  CommandOp = 0x04
	CmdHeartbeat    CommandOp = 0x05
)

var (
	ErrInvalidMagic    = errors.New("invalid protocol magic byte")
	ErrCRC32Mismatch   = errors.New("crc32 checksum verification failed: data corruption detected")
	ErrPayloadTooLarge = errors.New("frame payload exceeds maximum allowable size")
)

// Frame represents a single parsed or outgoing KhorosLog binary message.
type Frame struct {
	Magic   byte
	Length  uint32
	Offset  uint64
	CRC32   uint32
	Payload []byte
}

// EncodeFrame serializes a message payload and offset into a raw 17-byte framed binary slice.
// Layout: [Magic (1B)][Length (4B)][Offset (8B)][CRC32 (4B)][Payload (NB)]
func EncodeFrame(offset uint64, payload []byte) ([]byte, error) {
	payloadLen := uint32(len(payload))
	if payloadLen > MaxPayloadSize {
		return nil, fmt.Errorf("%w: %d > %d", ErrPayloadTooLarge, payloadLen, MaxPayloadSize)
	}

	totalLen := HeaderSize + int(payloadLen)
	buf := make([]byte, totalLen)

	// Magic Byte (1B)
	buf[0] = MagicByte

	// Message Length (4B)
	binary.BigEndian.PutUint32(buf[1:5], payloadLen)

	// Offset (8B)
	binary.BigEndian.PutUint64(buf[5:13], offset)

	// CRC32 (4B) computed over the payload
	checksum := crc32.ChecksumIEEE(payload)
	binary.BigEndian.PutUint32(buf[13:17], checksum)

	// Payload (NB)
	copy(buf[17:], payload)

	return buf, nil
}

// DecodeFrame reads a full KhorosLog frame from an io.Reader.
// It handles partial TCP reads correctly by utilizing io.ReadFull, and validates CRC32.
func DecodeFrame(r io.Reader) (*Frame, error) {
	headerBuf := make([]byte, HeaderSize)
	if _, err := io.ReadFull(r, headerBuf); err != nil {
		return nil, err
	}

	magic := headerBuf[0]
	if magic != MagicByte {
		return nil, fmt.Errorf("%w: received 0x%02X, expected 0x%02X", ErrInvalidMagic, magic, MagicByte)
	}

	length := binary.BigEndian.Uint32(headerBuf[1:5])
	if length > MaxPayloadSize {
		return nil, fmt.Errorf("%w: %d > %d", ErrPayloadTooLarge, length, MaxPayloadSize)
	}

	offset := binary.BigEndian.Uint64(headerBuf[5:13])
	expectedCRC := binary.BigEndian.Uint32(headerBuf[13:17])

	payload := make([]byte, length)
	if length > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return nil, err
		}
	}

	actualCRC := crc32.ChecksumIEEE(payload)
	if actualCRC != expectedCRC {
		return nil, fmt.Errorf("%w: expected 0x%08X, got 0x%08X", ErrCRC32Mismatch, expectedCRC, actualCRC)
	}

	return &Frame{
		Magic:   magic,
		Length:  length,
		Offset:  offset,
		CRC32:   expectedCRC,
		Payload: payload,
	}, nil
}

// WriteFrame encodes and writes a complete frame directly to an io.Writer.
func WriteFrame(w io.Writer, offset uint64, payload []byte) error {
	buf, err := EncodeFrame(offset, payload)
	if err != nil {
		return err
	}
	_, err = w.Write(buf)
	return err
}
