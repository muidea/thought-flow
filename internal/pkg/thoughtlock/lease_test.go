package thoughtlock

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLeaseHeartbeatAndUniqueJobOwnership(t *testing.T) {
	locker := New(60 * time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	release, err := locker.Lease(ctx, "thought", "refiner:one")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	time.Sleep(130 * time.Millisecond)
	if err := locker.Acquire("thought", "refiner:two"); !errors.Is(err, ErrLocked) {
		t.Fatal("heartbeat did not retain lease", err)
	}
	locker.Release("thought", "refiner:two")
	if holder, ok := locker.Holder("thought"); !ok || holder != "refiner:one" {
		t.Fatal("sibling released running job")
	}
	wait, cancelWait := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancelWait()
	if _, err = locker.Lease(wait, "thought", "expander:other"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
