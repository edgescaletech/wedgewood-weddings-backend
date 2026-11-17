package jobs

import (
	"context"
	"testing"
	"time"
)

func TestEventSyncWorker_Deduplication(t *testing.T) {
	worker := NewEventSyncWorker(nil, nil)

	start := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 11, 22, 23, 59, 59, 0, time.UTC)
	key := start.Format("2006-01-02")

	// Manually insert an in-flight key to simulate active sync
	doneCh := make(chan struct{})
	worker.runningMu.Lock()
	worker.running[key] = doneCh
	worker.runningMu.Unlock()

	// 1. SyncWeekAsync should return false when a sync is already running
	started := worker.SyncWeekAsync(start, end)
	if started {
		t.Errorf("SyncWeekAsync returned true for already running week, expected false")
	}

	// 2. SyncWeek with cancelled context should exit immediately when waiting for in-flight sync
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	err := worker.SyncWeek(ctx, start, end)
	if err != context.Canceled {
		t.Errorf("SyncWeek with cancelled context: got %v, want %v", err, context.Canceled)
	}

	// Clean up simulated sync
	close(doneCh)
	worker.runningMu.Lock()
	delete(worker.running, key)
	worker.runningMu.Unlock()
}

func TestEventSyncWorker_SemaphoreLimit(t *testing.T) {
	worker := NewEventSyncWorker(nil, nil)

	// Verify semaphore initial capacity is 2
	if cap(worker.sem) != 2 {
		t.Fatalf("worker semaphore capacity: got %d, want 2", cap(worker.sem))
	}

	// Fill the semaphore tokens
	worker.sem <- struct{}{}
	worker.sem <- struct{}{}

	// A 3rd attempt to acquire with a short timeout should fail with deadline exceeded
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	start := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 11, 29, 23, 59, 59, 0, time.UTC)

	err := worker.SyncWeek(ctx, start, end)
	if err != context.DeadlineExceeded {
		t.Errorf("expected context.DeadlineExceeded when semaphore is full, got %v", err)
	}

	// Release tokens
	<-worker.sem
	<-worker.sem
}
