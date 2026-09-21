package tests

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"KhorosLog/storage"
)

func TestWAL_AppendAndRead(t *testing.T) {
	tempDir := t.TempDir()

	cfg := storage.Config{
		DataDir:            tempDir,
		MaxSegmentBytes:    1024 * 1024, // 1MB for test
		IndexIntervalBytes: 128,         // Frequent sparse checkpoints
		SyncOnWrite:        false,
	}

	wal, err := storage.OpenWAL(cfg)
	if err != nil {
		t.Fatalf("OpenWAL failed: %v", err)
	}
	defer wal.Close()

	numMsgs := 100
	for i := 0; i < numMsgs; i++ {
		payload := []byte(fmt.Sprintf("message-payload-index-%d", i))
		offset, err := wal.Append(payload)
		if err != nil {
			t.Fatalf("Append failed at %d: %v", i, err)
		}
		if offset != uint64(i) {
			t.Fatalf("expected offset %d, got %d", i, offset)
		}
	}

	for i := 0; i < numMsgs; i++ {
		expected := []byte(fmt.Sprintf("message-payload-index-%d", i))
		got, err := wal.ReadAt(uint64(i))
		if err != nil {
			t.Fatalf("ReadAt(%d) failed: %v", i, err)
		}
		if !bytes.Equal(got, expected) {
			t.Fatalf("offset %d mismatch: got %s, want %s", i, got, expected)
		}
	}
}

func TestWAL_SegmentRolling(t *testing.T) {
	tempDir := t.TempDir()

	cfg := storage.Config{
		DataDir:            tempDir,
		MaxSegmentBytes:    512, // 512 bytes forces fast rolling
		IndexIntervalBytes: 64,
		SyncOnWrite:        false,
	}

	wal, err := storage.OpenWAL(cfg)
	if err != nil {
		t.Fatalf("OpenWAL failed: %v", err)
	}

	numMsgs := 50
	for i := 0; i < numMsgs; i++ {
		payload := []byte(fmt.Sprintf("rolling-test-msg-%03d", i))
		_, err := wal.Append(payload)
		if err != nil {
			t.Fatalf("Append failed at %d: %v", i, err)
		}
	}

	for i := 0; i < numMsgs; i++ {
		expected := []byte(fmt.Sprintf("rolling-test-msg-%03d", i))
		got, err := wal.ReadAt(uint64(i))
		if err != nil {
			t.Fatalf("ReadAt(%d) across rolled segments failed: %v", i, err)
		}
		if !bytes.Equal(got, expected) {
			t.Fatalf("offset %d mismatch: got %s, want %s", i, got, expected)
		}
	}

	wal.Close()

	entries, err := os.ReadDir(tempDir)
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}

	logCount := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".log" {
			logCount++
		}
	}

	if logCount < 3 {
		t.Fatalf("expected at least 3 rolled segments, got %d", logCount)
	}
}

func TestWAL_RestartAndRecovery(t *testing.T) {
	tempDir := t.TempDir()

	cfg := storage.Config{
		DataDir:            tempDir,
		MaxSegmentBytes:    4096,
		IndexIntervalBytes: 256,
		SyncOnWrite:        true,
	}

	wal1, err := storage.OpenWAL(cfg)
	if err != nil {
		t.Fatalf("OpenWAL failed: %v", err)
	}

	for i := 0; i < 20; i++ {
		_, err := wal1.Append([]byte(fmt.Sprintf("persistent-msg-%d", i)))
		if err != nil {
			t.Fatalf("Append failed: %v", err)
		}
	}
	wal1.Close()

	wal2, err := storage.OpenWAL(cfg)
	if err != nil {
		t.Fatalf("reopening WAL failed: %v", err)
	}
	defer wal2.Close()

	if wal2.LatestOffset() != 20 {
		t.Fatalf("expected latest offset 20 after restart, got %d", wal2.LatestOffset())
	}

	for i := 0; i < 20; i++ {
		got, err := wal2.ReadAt(uint64(i))
		if err != nil {
			t.Fatalf("ReadAt(%d) after restart failed: %v", i, err)
		}
		expected := []byte(fmt.Sprintf("persistent-msg-%d", i))
		if !bytes.Equal(got, expected) {
			t.Fatalf("mismatch at %d after restart", i)
		}
	}

	newOffset, err := wal2.Append([]byte("post-restart-message"))
	if err != nil {
		t.Fatalf("Append after restart failed: %v", err)
	}
	if newOffset != 20 {
		t.Fatalf("expected next monotonic offset 20, got %d", newOffset)
	}
}
