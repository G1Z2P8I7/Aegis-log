package storage

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// IndexEntrySize is the 8-byte layout:
// [RelativeOffset (4B)] + [PhysicalPosition (4B)]
const IndexEntrySize = 8

// SparseIndex manages a memory-mapped binary index file (.index) for rapid O(1) offset seeks.
type SparseIndex struct {
	mu         sync.RWMutex
	file       *os.File
	mmap       *mmapHandle
	entryCount int
	maxEntries int
	path       string
}

// OpenSparseIndex initializes or opens an existing .index file.
func OpenSparseIndex(path string, maxEntries int) (*SparseIndex, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}

	totalSize := int64(maxEntries * IndexEntrySize)

	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open index file: %w", err)
	}

	// Ensure file is preallocated to totalSize for mmap
	fi, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, err
	}

	actualEntries := int(fi.Size() / IndexEntrySize)
	if fi.Size() < totalSize {
		if err := file.Truncate(totalSize); err != nil {
			file.Close()
			return nil, fmt.Errorf("failed to preallocate index file: %w", err)
		}
	}

	mh, err := mapFileWin32(file, totalSize, false)
	if err != nil {
		file.Close()
		return nil, fmt.Errorf("failed to mmap index file: %w", err)
	}

	return &SparseIndex{
		file:       file,
		mmap:       mh,
		entryCount: actualEntries,
		maxEntries: maxEntries,
		path:       path,
	}, nil
}

// Append writes a new [RelativeOffset, PhysicalPos] pair to the memory-mapped index.
func (idx *SparseIndex) Append(relOffset uint32, physicalPos uint32) error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if idx.entryCount >= idx.maxEntries {
		return fmt.Errorf("sparse index is full (%d entries)", idx.entryCount)
	}

	entryOffset := idx.entryCount * IndexEntrySize
	binary.BigEndian.PutUint32(idx.mmap.data[entryOffset:entryOffset+4], relOffset)
	binary.BigEndian.PutUint32(idx.mmap.data[entryOffset+4:entryOffset+8], physicalPos)
	idx.entryCount++

	return nil
}

// Lookup performs a binary search over the index to find the largest indexed entry <= targetRelOffset.
// Returns physical file position inside the segment.
func (idx *SparseIndex) Lookup(targetRelOffset uint32) (physicalPos uint32, found bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()

	if idx.entryCount == 0 {
		return 0, false
	}

	// Binary search for entry with relativeOffset <= targetRelOffset
	i := sort.Search(idx.entryCount, func(i int) bool {
		offset := i * IndexEntrySize
		entryRel := binary.BigEndian.Uint32(idx.mmap.data[offset : offset+4])
		return entryRel > targetRelOffset
	})

	if i == 0 {
		firstOffset := binary.BigEndian.Uint32(idx.mmap.data[0:4])
		if targetRelOffset >= firstOffset {
			pos := binary.BigEndian.Uint32(idx.mmap.data[4:8])
			return pos, true
		}
		return 0, false
	}

	matchedEntry := (i - 1) * IndexEntrySize
	pos := binary.BigEndian.Uint32(idx.mmap.data[matchedEntry+4 : matchedEntry+8])
	return pos, true
}

// Close shrinks the file to its actual used entry count, flushes, and closes the mmap.
func (idx *SparseIndex) Close() error {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if idx.mmap == nil {
		return nil
	}

	var firstErr error
	if err := idx.mmap.Flush(); err != nil && firstErr == nil {
		firstErr = err
	}

	usedBytes := int64(idx.entryCount * IndexEntrySize)
	if err := idx.mmap.Close(); err != nil && firstErr == nil {
		firstErr = err
	}
	idx.mmap = nil

	if idx.file != nil {
		_ = idx.file.Truncate(usedBytes)
		_ = idx.file.Close()
		idx.file = nil
	}

	return firstErr
}
