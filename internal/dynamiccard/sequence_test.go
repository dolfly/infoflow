package dynamiccard

import (
	"context"
	"testing"
	"time"
)

// T-06: a successful response overwrites the local sequence and idles the card.
func TestGateSuccessOverwritesSequence(t *testing.T) {
	gate := NewGate()
	release, err := gate.Lock(context.Background(), "card-1")
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	if snapshot := gate.Snapshot("card-1"); snapshot.State != GateInflight {
		t.Fatalf("state while locked = %q, want %q", snapshot.State, GateInflight)
	}
	gate.Succeed("card-1", 3)
	release()

	snapshot := gate.Snapshot("card-1")
	if !snapshot.HasSequence || snapshot.Sequence != 3 {
		t.Fatalf("sequence = %d/%v, want 3/true", snapshot.Sequence, snapshot.HasSequence)
	}
	if snapshot.State != GateIdle {
		t.Fatalf("state = %q, want %q", snapshot.State, GateIdle)
	}
}

// T-07: 1009 adopts current_sequence, pauses the card and never replays.
func TestGateSequenceConflictAdoptsCurrentAndPauses(t *testing.T) {
	gate := NewGate()
	gate.Succeed("card-1", 2)

	release, err := gate.Lock(context.Background(), "card-1")
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	current := int64(5)
	gate.Fail("card-1", 1009, &current)
	release()

	snapshot := gate.Snapshot("card-1")
	if snapshot.Sequence != 5 || !snapshot.HasSequence {
		t.Fatalf("sequence = %d/%v, want 5/true", snapshot.Sequence, snapshot.HasSequence)
	}
	if snapshot.State != GatePaused {
		t.Fatalf("state = %q, want %q (a pause must not be reset by release)", snapshot.State, GatePaused)
	}
	// The gate has no replay path: a paused card stays paused until the caller
	// records the next explicit outcome.
	if snapshot := gate.Snapshot("card-1"); snapshot.State != GatePaused {
		t.Fatalf("state changed without a caller action: %q", snapshot.State)
	}
}

// T-08: 6001 never rolls the sequence back.
func TestGateRateLimitedKeepsSequence(t *testing.T) {
	gate := NewGate()
	gate.Succeed("card-1", 4)
	gate.Fail("card-1", 6001, nil)

	snapshot := gate.Snapshot("card-1")
	if !snapshot.HasSequence || snapshot.Sequence != 4 {
		t.Fatalf("sequence = %d/%v, want 4/true", snapshot.Sequence, snapshot.HasSequence)
	}
	if snapshot.State != GateIdle {
		t.Fatalf("state = %q, want %q", snapshot.State, GateIdle)
	}
}

func TestGateStreamStateExpiresCard(t *testing.T) {
	gate := NewGate()
	gate.Succeed("card-1", 1)
	gate.Fail("card-1", 1010, nil)
	if snapshot := gate.Snapshot("card-1"); snapshot.State != GateExpired {
		t.Fatalf("state = %q, want %q", snapshot.State, GateExpired)
	}
	if snapshot := gate.Snapshot("card-1"); snapshot.Sequence != 1 {
		t.Fatalf("sequence = %d, want 1 (1010 must not touch it)", snapshot.Sequence)
	}
}

func TestGateUnknownFailurePausesCard(t *testing.T) {
	gate := NewGate()
	gate.Succeed("card-1", 2)
	gate.Fail("card-1", 1234, nil)
	if snapshot := gate.Snapshot("card-1"); snapshot.State != GatePaused {
		t.Fatalf("state = %q, want %q", snapshot.State, GatePaused)
	}
}

// T-09: the same card is serialized; different cards run in parallel.
func TestGateSerializesSameCard(t *testing.T) {
	gate := NewGate()
	release, err := gate.Lock(context.Background(), "card-1")
	if err != nil {
		t.Fatalf("lock: %v", err)
	}

	acquired := make(chan struct{})
	go func() {
		nextRelease, lockErr := gate.Lock(context.Background(), "card-1")
		if lockErr != nil {
			return
		}
		close(acquired)
		nextRelease()
	}()

	select {
	case <-acquired:
		t.Fatal("second lock for the same card was acquired while the first was held")
	case <-time.After(50 * time.Millisecond):
	}

	release()
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("second lock did not proceed after the first was released")
	}
}

func TestGateRunsDifferentCardsInParallel(t *testing.T) {
	gate := NewGate()
	first, err := gate.Lock(context.Background(), "card-1")
	if err != nil {
		t.Fatalf("lock card-1: %v", err)
	}
	defer first()

	acquired := make(chan struct{})
	go func() {
		second, lockErr := gate.Lock(context.Background(), "card-2")
		if lockErr != nil {
			return
		}
		close(acquired)
		second()
	}()

	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("a different card was blocked by card-1")
	}
}

func TestGateLockHonoursContext(t *testing.T) {
	gate := NewGate()
	release, err := gate.Lock(context.Background(), "card-1")
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := gate.Lock(ctx, "card-1"); err == nil {
		t.Fatal("lock with an expired context returned no error")
	}

	if _, err := gate.Lock(context.Background(), "  "); err == nil {
		t.Fatal("blank card id returned no error")
	}
	if _, err := gate.Lock(nil, "card-2"); err == nil { //nolint:staticcheck // nil context is the case under test
		t.Fatal("nil context returned no error")
	}
}

func TestGateReleaseIsIdempotent(t *testing.T) {
	gate := NewGate()
	release, err := gate.Lock(context.Background(), "card-1")
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	release()
	release()
	if snapshot := gate.Snapshot("card-1"); snapshot.State != GateIdle {
		t.Fatalf("state = %q, want %q", snapshot.State, GateIdle)
	}
}

func TestGateSnapshotOfUnknownCard(t *testing.T) {
	snapshot := NewGate().Snapshot("missing")
	if snapshot.State != GateIdle || snapshot.HasSequence {
		t.Fatalf("snapshot = %+v, want idle without sequence", snapshot)
	}
}
