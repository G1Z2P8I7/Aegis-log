package tests

import (
	"testing"
	"time"

	"KhorosLog/consumer"
)

func TestCoordinator_Rebalance(t *testing.T) {
	tempDir := t.TempDir()
	coord, err := consumer.NewCoordinator(tempDir, 2*time.Second)
	if err != nil {
		t.Fatalf("NewCoordinator failed: %v", err)
	}
	defer coord.Close()

	partitions := []string{"part-0", "part-1", "part-2", "part-3"}

	// Member 1 joins -> should receive all 4 partitions
	parts1, err := coord.JoinGroup("analytics-group", "consumer-1", partitions)
	if err != nil {
		t.Fatalf("JoinGroup consumer-1 failed: %v", err)
	}
	if len(parts1) != 4 {
		t.Fatalf("expected 4 partitions, got %d", len(parts1))
	}

	// Member 2 joins -> partitions split (2 and 2)
	parts2, err := coord.JoinGroup("analytics-group", "consumer-2", partitions)
	if err != nil {
		t.Fatalf("JoinGroup consumer-2 failed: %v", err)
	}
	if len(parts2) != 2 {
		t.Fatalf("expected 2 partitions for consumer-2, got %d", len(parts2))
	}

	// Member 1 leaves -> consumer-2 receives all 4 partitions
	err = coord.LeaveGroup("analytics-group", "consumer-1", partitions)
	if err != nil {
		t.Fatalf("LeaveGroup consumer-1 failed: %v", err)
	}

	// Fetch updated assignments for consumer-2 by heartbeat or rejoin
	parts2Updated, err := coord.JoinGroup("analytics-group", "consumer-2", partitions)
	if err != nil {
		t.Fatalf("rejoin failed: %v", err)
	}
	if len(parts2Updated) != 4 {
		t.Fatalf("expected consumer-2 to have 4 partitions after consumer-1 left, got %d", len(parts2Updated))
	}
}

func TestCoordinator_OffsetPersistenceAndRecovery(t *testing.T) {
	tempDir := t.TempDir()

	// Session 1: Commit offsets and close
	coord1, err := consumer.NewCoordinator(tempDir, 5*time.Second)
	if err != nil {
		t.Fatalf("NewCoordinator failed: %v", err)
	}

	if err := coord1.CommitOffset("payment-processors", "partition-0", 4200); err != nil {
		t.Fatalf("CommitOffset failed: %v", err)
	}
	if err := coord1.CommitOffset("payment-processors", "partition-1", 8500); err != nil {
		t.Fatalf("CommitOffset failed: %v", err)
	}
	coord1.Close()

	// Session 2: Reopen coordinator and verify offsets survived crash/restart
	coord2, err := consumer.NewCoordinator(tempDir, 5*time.Second)
	if err != nil {
		t.Fatalf("reopening coordinator failed: %v", err)
	}
	defer coord2.Close()

	off0, err := coord2.FetchOffset("payment-processors", "partition-0")
	if err != nil || off0 != 4200 {
		t.Fatalf("expected offset 4200, got %d (err: %v)", off0, err)
	}

	off1, err := coord2.FetchOffset("payment-processors", "partition-1")
	if err != nil || off1 != 8500 {
		t.Fatalf("expected offset 8500, got %d (err: %v)", off1, err)
	}
}
