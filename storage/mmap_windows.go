//go:build windows

package storage

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// mmapHandle encapsulates Win32 memory-mapped file descriptors and pointer.
type mmapHandle struct {
	file   *os.File
	hMap   windows.Handle
	addr   uintptr
	data   []byte
	size   int64
	isOpen bool
}

// mapFileWin32 creates or opens a Windows memory-mapped view of the specified file with the given size.
func mapFileWin32(file *os.File, size int64, readOnly bool) (*mmapHandle, error) {
	protect := uint32(windows.PAGE_READWRITE)
	access := uint32(windows.FILE_MAP_READ | windows.FILE_MAP_WRITE)
	if readOnly {
		protect = windows.PAGE_READONLY
		access = windows.FILE_MAP_READ
	}

	highSize := uint32(size >> 32)
	lowSize := uint32(size & 0xFFFFFFFF)

	hMap, err := windows.CreateFileMapping(
		windows.Handle(file.Fd()),
		nil,
		protect,
		highSize,
		lowSize,
		nil,
	)
	if err != nil && err != windows.ERROR_ALREADY_EXISTS {
		return nil, fmt.Errorf("CreateFileMapping failed: %w", err)
	}

	addr, err := windows.MapViewOfFile(
		hMap,
		access,
		0,
		0,
		uintptr(size),
	)
	if err != nil {
		windows.CloseHandle(hMap)
		return nil, fmt.Errorf("MapViewOfFile failed: %w", err)
	}

	// Construct a slice header pointing to the mapped address space
	sliceHeader := struct {
		addr uintptr
		len  int
		cap  int
	}{uintptr(addr), int(size), int(size)}
	data := *(*[]byte)(unsafe.Pointer(&sliceHeader))

	return &mmapHandle{
		file:   file,
		hMap:   hMap,
		addr:   addr,
		data:   data,
		size:   size,
		isOpen: true,
	}, nil
}

// Flush synchronizes the dirty mapped pages to disk and commits the file buffers.
func (m *mmapHandle) Flush() error {
	if !m.isOpen || m.addr == 0 {
		return nil
	}

	if err := windows.FlushViewOfFile(m.addr, uintptr(m.size)); err != nil {
		return fmt.Errorf("FlushViewOfFile failed: %w", err)
	}
	if err := windows.FlushFileBuffers(windows.Handle(m.file.Fd())); err != nil {
		return fmt.Errorf("FlushFileBuffers failed: %w", err)
	}
	return nil
}

// Close unmaps the memory view, closes the mapping handle, and closes the underlying file.
func (m *mmapHandle) Close() error {
	if !m.isOpen {
		return nil
	}
	m.isOpen = false

	var firstErr error
	if m.addr != 0 {
		if err := windows.UnmapViewOfFile(m.addr); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("UnmapViewOfFile failed: %w", err)
		}
		m.addr = 0
	}

	if m.hMap != 0 {
		if err := windows.CloseHandle(m.hMap); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("CloseHandle failed: %w", err)
		}
		m.hMap = 0
	}

	if m.file != nil {
		if err := m.file.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("file close failed: %w", err)
		}
	}

	return firstErr
}
