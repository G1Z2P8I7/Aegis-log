package consumer

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

var (
	ErrGroupNotFound  = errors.New("consumer group not found")
	ErrMemberNotFound = errors.New("consumer member not found")
	ErrNoOffsetSaved  = errors.New("no committed offset found for group")
)

// OffsetRecord represents a durable offset checkpoint on disk.
type OffsetRecord struct {
	GroupID   string    `json:"group_id"`
	Partition string    `json:"partition"`
	Offset    uint64    `json:"offset"`
	Timestamp time.Time `json:"timestamp"`
}

// GroupMember tracks a single active subscriber connection in a group.
type GroupMember struct {
	ID             string    `json:"id"`
	AssignedParts  []string  `json:"assigned_partitions"`
	LastHeartbeat  time.Time `json:"last_heartbeat"`
}

// ConsumerGroup maintains active members and committed offsets for a subscriber pool.
type ConsumerGroup struct {
	ID         string
	mu         sync.RWMutex
	Members    map[string]*GroupMember
	Offsets    map[string]uint64 // partition -> offset
	Generation uint64
}

// Coordinator manages multiple consumer groups and persists offsets durably to disk.
type Coordinator struct {
	mu             sync.RWMutex
	dataDir        string
	offsetFile     *os.File
	offsetWriter   *bufio.Writer
	groups         map[string]*ConsumerGroup
	sessionTimeout time.Duration
	stopCh         chan struct{}
}

// NewCoordinator creates or restores a consumer group coordinator.
func NewCoordinator(dataDir string, sessionTimeout time.Duration) (*Coordinator, error) {
	if sessionTimeout == 0 {
		sessionTimeout = 5 * time.Second
	}

	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, err
	}

	offsetPath := filepath.Join(dataDir, "offsets.checkpoint")
	file, err := os.OpenFile(offsetPath, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open offsets file: %w", err)
	}

	coord := &Coordinator{
		dataDir:        dataDir,
		offsetFile:     file,
		offsetWriter:   bufio.NewWriter(file),
		groups:         make(map[string]*ConsumerGroup),
		sessionTimeout: sessionTimeout,
		stopCh:         make(chan struct{}),
	}

	if err := coord.recoverOffsets(offsetPath); err != nil {
		file.Close()
		return nil, fmt.Errorf("failed to recover offsets: %w", err)
	}

	go coord.rebalanceEvictionLoop()

	return coord, nil
}

// recoverOffsets reads historical offset commit records from disk.
func (c *Coordinator) recoverOffsets(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var rec OffsetRecord
		if err := json.Unmarshal(line, &rec); err == nil {
			g, exists := c.groups[rec.GroupID]
			if !exists {
				g = &ConsumerGroup{
					ID:      rec.GroupID,
					Members: make(map[string]*GroupMember),
					Offsets: make(map[string]uint64),
				}
				c.groups[rec.GroupID] = g
			}
			g.Offsets[rec.Partition] = rec.Offset
		}
	}

	return scanner.Err()
}

// JoinGroup registers a consumer member with a group and triggers rebalancing.
func (c *Coordinator) JoinGroup(groupID, memberID string, availablePartitions []string) ([]string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	g, exists := c.groups[groupID]
	if !exists {
		g = &ConsumerGroup{
			ID:      groupID,
			Members: make(map[string]*GroupMember),
			Offsets: make(map[string]uint64),
		}
		c.groups[groupID] = g
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	g.Members[memberID] = &GroupMember{
		ID:            memberID,
		LastHeartbeat: time.Now(),
	}
	g.Generation++

	c.rebalanceGroup(g, availablePartitions)
	return g.Members[memberID].AssignedParts, nil
}

// LeaveGroup removes a member and triggers immediate partition rebalancing.
func (c *Coordinator) LeaveGroup(groupID, memberID string, availablePartitions []string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	g, exists := c.groups[groupID]
	if !exists {
		return ErrGroupNotFound
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	delete(g.Members, memberID)
	g.Generation++

	c.rebalanceGroup(g, availablePartitions)
	return nil
}

// Heartbeat updates member liveness timestamp to prevent eviction.
func (c *Coordinator) Heartbeat(groupID, memberID string) error {
	c.mu.RLock()
	g, exists := c.groups[groupID]
	c.mu.RUnlock()

	if !exists {
		return ErrGroupNotFound
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	m, exists := g.Members[memberID]
	if !exists {
		return ErrMemberNotFound
	}

	m.LastHeartbeat = time.Now()
	return nil
}

// CommitOffset durably persists a consumer's read position to disk and in memory.
func (c *Coordinator) CommitOffset(groupID, partition string, offset uint64) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	g, exists := c.groups[groupID]
	if !exists {
		g = &ConsumerGroup{
			ID:      groupID,
			Members: make(map[string]*GroupMember),
			Offsets: make(map[string]uint64),
		}
		c.groups[groupID] = g
	}

	rec := OffsetRecord{
		GroupID:   groupID,
		Partition: partition,
		Offset:    offset,
		Timestamp: time.Now(),
	}

	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}

	if _, err := c.offsetWriter.Write(append(line, '\n')); err != nil {
		return err
	}
	if err := c.offsetWriter.Flush(); err != nil {
		return err
	}
	if err := c.offsetFile.Sync(); err != nil {
		return err
	}

	g.mu.Lock()
	g.Offsets[partition] = offset
	g.mu.Unlock()

	return nil
}

// FetchOffset retrieves the last durably committed offset for a group and partition.
func (c *Coordinator) FetchOffset(groupID, partition string) (uint64, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	g, exists := c.groups[groupID]
	if !exists {
		return 0, ErrGroupNotFound
	}

	g.mu.RLock()
	defer g.mu.RUnlock()

	offset, exists := g.Offsets[partition]
	if !exists {
		return 0, ErrNoOffsetSaved
	}

	return offset, nil
}

// rebalanceGroup distributes partitions evenly across active members. Must be called with g.mu locked.
func (c *Coordinator) rebalanceGroup(g *ConsumerGroup, partitions []string) {
	if len(g.Members) == 0 {
		return
	}

	memberIDs := make([]string, 0, len(g.Members))
	for id := range g.Members {
		memberIDs = append(memberIDs, id)
		g.Members[id].AssignedParts = nil
	}
	sort.Strings(memberIDs)

	for i, part := range partitions {
		targetMember := memberIDs[i%len(memberIDs)]
		g.Members[targetMember].AssignedParts = append(g.Members[targetMember].AssignedParts, part)
	}
}

// rebalanceEvictionLoop detects crashed/disconnected consumers and evicts them.
func (c *Coordinator) rebalanceEvictionLoop() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.stopCh:
			return
		case <-ticker.C:
			c.mu.Lock()
			now := time.Now()
			for _, g := range c.groups {
				g.mu.Lock()
				evicted := false
				for id, m := range g.Members {
					if now.Sub(m.LastHeartbeat) > c.sessionTimeout {
						delete(g.Members, id)
						evicted = true
					}
				}
				if evicted {
					g.Generation++
				}
				g.mu.Unlock()
			}
			c.mu.Unlock()
		}
	}
}

// Close flushes data and closes the persistence file.
func (c *Coordinator) Close() error {
	close(c.stopCh)
	c.mu.Lock()
	defer c.mu.Unlock()

	_ = c.offsetWriter.Flush()
	return c.offsetFile.Close()
}
