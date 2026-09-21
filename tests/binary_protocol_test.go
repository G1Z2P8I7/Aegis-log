package tests

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"KhorosLog/network"
)

// slowReader simulates fragmented TCP packet delivery by reading only chunk bytes at a time.
type slowReader struct {
	data      []byte
	chunkSize int
	pos       int
}

func (s *slowReader) Read(p []byte) (int, error) {
	if s.pos >= len(s.data) {
		return 0, io.EOF
	}
	remaining := len(s.data) - s.pos
	toRead := min(min(len(p), s.chunkSize), remaining)
	copy(p, s.data[s.pos:s.pos+toRead])
	s.pos += toRead
	return toRead, nil
}

func TestBinaryProtocol_RoundTrip(t *testing.T) {
	offset := uint64(1042)
	payload := []byte("high-throughput-event-stream-data-payload-123456789")

	encoded, err := network.EncodeFrame(offset, payload)
	if err != nil {
		t.Fatalf("EncodeFrame failed: %v", err)
	}

	if len(encoded) != network.HeaderSize+len(payload) {
		t.Fatalf("unexpected frame size: got %d, expected %d", len(encoded), network.HeaderSize+len(payload))
	}

	buf := bytes.NewReader(encoded)
	decoded, err := network.DecodeFrame(buf)
	if err != nil {
		t.Fatalf("DecodeFrame failed: %v", err)
	}

	if decoded.Magic != network.MagicByte {
		t.Errorf("magic byte mismatch: got 0x%02X", decoded.Magic)
	}
	if decoded.Offset != offset {
		t.Errorf("offset mismatch: got %d, want %d", decoded.Offset, offset)
	}
	if decoded.Length != uint32(len(payload)) {
		t.Errorf("length mismatch: got %d, want %d", decoded.Length, len(payload))
	}
	if !bytes.Equal(decoded.Payload, payload) {
		t.Errorf("payload content mismatch")
	}
}

func TestBinaryProtocol_FragmentedTCPRead(t *testing.T) {
	offset := uint64(9999)
	payload := []byte("simulating-fragmented-tcp-stream-across-multiple-read-calls")

	encoded, err := network.EncodeFrame(offset, payload)
	if err != nil {
		t.Fatalf("EncodeFrame failed: %v", err)
	}

	// Deliver the frame in tiny 3-byte chunks to test io.ReadFull robustness
	reader := &slowReader{
		data:      encoded,
		chunkSize: 3,
	}

	decoded, err := network.DecodeFrame(reader)
	if err != nil {
		t.Fatalf("DecodeFrame failed on fragmented TCP stream: %v", err)
	}

	if decoded.Offset != offset || !bytes.Equal(decoded.Payload, payload) {
		t.Fatalf("decoded frame failed verification under fragmented reads")
	}
}

func TestBinaryProtocol_CRC32Corruption(t *testing.T) {
	offset := uint64(500)
	payload := []byte("critical-unaltered-transaction-message")

	encoded, err := network.EncodeFrame(offset, payload)
	if err != nil {
		t.Fatalf("EncodeFrame failed: %v", err)
	}

	// Corrupt one single bit in the payload
	encoded[len(encoded)-1] ^= 0x01

	buf := bytes.NewReader(encoded)
	_, err = network.DecodeFrame(buf)
	if err == nil {
		t.Fatalf("expected CRC32 verification error, got nil")
	}
	if !errors.Is(err, network.ErrCRC32Mismatch) {
		t.Fatalf("expected ErrCRC32Mismatch, got: %v", err)
	}
}

func TestBinaryProtocol_InvalidMagic(t *testing.T) {
	offset := uint64(100)
	payload := []byte("test")

	encoded, _ := network.EncodeFrame(offset, payload)
	encoded[0] = 0xFF // Corrupt magic byte

	buf := bytes.NewReader(encoded)
	_, err := network.DecodeFrame(buf)
	if err == nil {
		t.Fatalf("expected ErrInvalidMagic, got nil")
	}
	if !errors.Is(err, network.ErrInvalidMagic) {
		t.Fatalf("expected ErrInvalidMagic, got: %v", err)
	}
}
