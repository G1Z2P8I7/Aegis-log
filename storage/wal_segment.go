package storage

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"KhorosLog/network"
)

var (
	ErrOffsetNotFound = errors.New("requested offset not found in WAL")
	ErrSegmentFull    = errors.New("segment is full")
	ErrWALClosed      = errors.New("wal is closed")
)

// Config defines the operational parameters for the WAL engine.
type Config struct {
	DataDir            string
	MaxSegmentBytes    int64
	IndexIntervalBytes uint32
	SyncOnWrite        bool
}

// DefaultConfig returns default production settings (64MB segment rolling).
func DefaultConfig(dataDir string) Config {
	return Config{
		DataDir:            dataDir,
		MaxSegmentBytes:    64 * 1024 * 1024, // 64MB
		IndexIntervalBytes: 4096,             // 4KB sparse checkpoints
		SyncOnWrite:        false,
	}
}

// Segment represents a single immutable or active memory-mapped log file and its companion index.
type Segment struct {
	mu            sync.RWMutex
	baseOffset    uint64
	nextOffset    uint64
	logFile       *os.File
	mmap          *mmapHandle
	index         *SparseIndex
	writePos      uint32
	maxBytes      int64
	lastIndexByte uint32
	indexInterval uint32
	isClosed      bool
	logPath       string
	indexPath     string
}

// NewSegment creates or maps an existing log segment and its sparse index.
func NewSegment(dir string, baseOffset uint64, maxBytes int64, indexInterval uint32) (*Segment, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	prefix := fmt.Sprintf("%020d", baseOffset)
	logPath := filepath.Join(dir, prefix+".log")
	indexPath := filepath.Join(dir, prefix+".index")

	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open segment log: %w", err)
	}

	fi, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}

	// Preallocate segment to maxBytes for zero-copy memory mapping
	if fi.Size() < maxBytes {
		if err := file.Truncate(maxBytes); err != nil {
			file.Close()
			return nil, fmt.Errorf("failed to preallocate log segment: %w", err)
		}
	}

	mh, err := mapFileWin32(file, maxBytes, false)
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("failed to mmap log segment: %w", err)
	}

	maxIndexEntries := int(maxBytes/int64(indexInterval)) + 16
	idx, err := OpenSparseIndex(indexPath, maxIndexEntries)
	if err != nil {
		_ = mh.Close()
		return nil, fmt.Errorf("failed to open companion index: %w", err)
	}

	seg := &Segment{
		baseOffset:    baseOffset,
		nextOffset:    baseOffset,
		logFile:       file,
		mmap:          mh,
		index:         idx,
		writePos:      0,
		maxBytes:      maxBytes,
		lastIndexByte: 0,
		indexInterval: indexInterval,
		isClosed:      false,
		logPath:       logPath,
		indexPath:     indexPath,
	}

	if err := seg.recover(); err != nil {
		_ = seg.Close()
		return nil, fmt.Errorf("failed to recover segment: %w", err)
	}

	return seg, nil
}

// recover scans through written frames to restore writePos and nextOffset upon restart.
func (s *Segment) recover() error {
	pos := uint32(0)
	for int64(pos+network.HeaderSize) <= s.maxBytes {
		magic := s.mmap.data[pos]
		if magic != network.MagicByte {
			break
		}

		reader := bytes.NewReader(s.mmap.data[pos:])
		frame, err := network.DecodeFrame(reader)
		if err != nil {
			break
		}

		frameLen := network.HeaderSize + frame.Length
		pos += frameLen
		s.nextOffset = frame.Offset + 1
	}
	s.writePos = pos
	return nil
}

// Append writes a frame directly to the mmap region, updating the sparse index as required.
func (s *Segment) Append(offset uint64, payload []byte) (uint32, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.isClosed {
		return 0, ErrWALClosed
	}

	encoded, err := network.EncodeFrame(offset, payload)
	if err != nil {
		return 0, err
	}

	frameLen := uint32(len(encoded))
	if int64(s.writePos+frameLen) > s.maxBytes {
		return 0, ErrSegmentFull
	}

	entryPos := s.writePos

	if s.writePos-s.lastIndexByte >= s.indexInterval || s.writePos == 0 {
		relOffset := uint32(offset - s.baseOffset)
		if err := s.index.Append(relOffset, entryPos); err == nil {
			s.lastIndexByte = entryPos
		}
	}

	copy(s.mmap.data[entryPos:entryPos+frameLen], encoded)
	s.writePos += frameLen
	s.nextOffset = offset + 1

	return entryPos, nil
}

// ReadAt retrieves the payload for the given offset using sparse index acceleration.
func (s *Segment) ReadAt(offset uint64) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if offset < s.baseOffset || offset >= s.nextOffset {
		return nil, ErrOffsetNotFound
	}

	relOffset := uint32(offset - s.baseOffset)
	startPos, _ := s.index.Lookup(relOffset)

	currentPos := startPos
	for currentPos < s.writePos {
		if s.mmap.data[currentPos] != network.MagicByte {
			break
		}

		r := bytes.NewReader(s.mmap.data[currentPos:s.writePos])
		frame, err := network.DecodeFrame(r)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, err
		}

		if frame.Offset == offset {
			out := make([]byte, len(frame.Payload))
			copy(out, frame.Payload)
			return out, nil
		}

		currentPos += network.HeaderSize + frame.Length
	}

	return nil, ErrOffsetNotFound
}

// Flush flushes memory-mapped pages and file buffers to NVMe storage.
func (s *Segment) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.isClosed {
		return nil
	}

	var firstErr error
	if err := s.mmap.Flush(); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := s.index.mmap.Flush(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// Close unmaps memory and seals the segment.
func (s *Segment) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.isClosed {
		return nil
	}
	s.isClosed = true

	var firstErr error
	if err := s.index.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	if err := s.mmap.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// WAL coordinates multiple rolling Segments into a unified linear log.
type WAL struct {
	mu             sync.RWMutex
	cfg            Config
	activeSegment  *Segment
	closedSegments []*Segment
	nextOffset     uint64
	isClosed       bool
}

// OpenWAL opens or initializes a WAL engine in the specified directory.
func OpenWAL(cfg Config) (*WAL, error) {
	if err := os.MkdirAll(cfg.DataDir, 0755); err != nil {
		return nil, err
	}

	entries, err := os.ReadDir(cfg.DataDir)
	if err != nil {
		return nil, err
	}

	var baseOffsets []uint64
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".log") {
			baseStr := strings.TrimSuffix(entry.Name(), ".log")
			if base, err := strconv.ParseUint(baseStr, 10, 64); err == nil {
				baseOffsets = append(baseOffsets, base)
			}
		}
	}

	sort.Slice(baseOffsets, func(i, j int) bool {
		return baseOffsets[i] < baseOffsets[j]
	})

	wal := &WAL{
		cfg: cfg,
	}

	if len(baseOffsets) == 0 {
		seg, err := NewSegment(cfg.DataDir, 0, cfg.MaxSegmentBytes, cfg.IndexIntervalBytes)
		if err != nil {
			return nil, err
		}
		wal.activeSegment = seg
		wal.nextOffset = 0
	} else {
		for i, base := range baseOffsets {
			seg, err := NewSegment(cfg.DataDir, base, cfg.MaxSegmentBytes, cfg.IndexIntervalBytes)
			if err != nil {
				return nil, err
			}
			if i == len(baseOffsets)-1 {
				wal.activeSegment = seg
				wal.nextOffset = seg.nextOffset
			} else {
				wal.closedSegments = append(wal.closedSegments, seg)
			}
		}
	}

	return wal, nil
}

// Append writes a payload to the active segment, rolling segments when size threshold is reached.
func (w *WAL) Append(payload []byte) (uint64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.isClosed {
		return 0, ErrWALClosed
	}

	offset := w.nextOffset
	frameLen := uint32(network.HeaderSize + len(payload))

	// Check if active segment needs rolling
	if int64(w.activeSegment.writePos+frameLen) > w.cfg.MaxSegmentBytes {
		if err := w.rollActiveSegment(); err != nil {
			return 0, fmt.Errorf("failed to roll segment: %w", err)
		}
	}

	_, err := w.activeSegment.Append(offset, payload)
	if err != nil {
		return 0, err
	}

	w.nextOffset = offset + 1

	if w.cfg.SyncOnWrite {
		_ = w.activeSegment.Flush()
	}

	return offset, nil
}

// rollActiveSegment rolls the current active segment into closedSegments and spawns a new one.
func (w *WAL) rollActiveSegment() error {
	_ = w.activeSegment.Flush()

	w.closedSegments = append(w.closedSegments, w.activeSegment)

	newBase := w.nextOffset
	newSeg, err := NewSegment(w.cfg.DataDir, newBase, w.cfg.MaxSegmentBytes, w.cfg.IndexIntervalBytes)
	if err != nil {
		return err
	}
	w.activeSegment = newSeg
	return nil
}

// ReadAt reads a payload at the specified offset across all segments.
func (w *WAL) ReadAt(offset uint64) ([]byte, error) {
	w.mu.RLock()
	defer w.mu.RUnlock()

	if w.isClosed {
		return nil, ErrWALClosed
	}

	if offset >= w.activeSegment.baseOffset {
		return w.activeSegment.ReadAt(offset)
	}

	for i := len(w.closedSegments) - 1; i >= 0; i-- {
		seg := w.closedSegments[i]
		if offset >= seg.baseOffset {
			return seg.ReadAt(offset)
		}
	}

	return nil, ErrOffsetNotFound
}

// LatestOffset returns the next monotonic offset that will be assigned.
func (w *WAL) LatestOffset() uint64 {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.nextOffset
}

// Sync commits all dirty memory-mapped pages to disk.
func (w *WAL) Sync() error {
	w.mu.RLock()
	defer w.mu.RUnlock()
	if w.isClosed || w.activeSegment == nil {
		return nil
	}
	return w.activeSegment.Flush()
}

// Close flushes and unmaps all segments in the WAL.
func (w *WAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.isClosed {
		return nil
	}
	w.isClosed = true

	var firstErr error
	if w.activeSegment != nil {
		if err := w.activeSegment.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	for _, seg := range w.closedSegments {
		if err := seg.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
