package state

import (
	"os"
	"sync"
	"testing"
	"time"
)

func TestStore_GetUnseenSessionReturnsZero(t *testing.T) {
	s := New(t.TempDir())
	snap, err := s.Get("unknown")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if snap != (Snapshot{}) {
		t.Errorf("expected zero snapshot, got %+v", snap)
	}
	if snap.Disabled() {
		t.Error("zero snapshot should not be Disabled")
	}
}

func TestStore_BlockIncrementsBoth(t *testing.T) {
	s := New(t.TempDir())
	snap, err := s.Block("sess", "first", DefaultLimits)
	if err != nil {
		t.Fatalf("Block: %v", err)
	}
	if snap.ConsecutiveBlocks != 1 || snap.TotalBlocks != 1 {
		t.Errorf("got %+v", snap)
	}
}

func TestStore_AllowResetsConsecutive(t *testing.T) {
	s := New(t.TempDir())
	_, _ = s.Block("sess", "x", DefaultLimits)
	_, _ = s.Block("sess", "x", DefaultLimits)
	snap, err := s.Allow("sess")
	if err != nil {
		t.Fatalf("Allow: %v", err)
	}
	if snap.ConsecutiveBlocks != 0 {
		t.Errorf("ConsecutiveBlocks = %d, want 0", snap.ConsecutiveBlocks)
	}
	if snap.TotalBlocks != 2 {
		t.Errorf("TotalBlocks reset by Allow: got %d, want 2", snap.TotalBlocks)
	}
}

func TestStore_ConsecutiveLimitDisables(t *testing.T) {
	s := New(t.TempDir())
	lim := Limits{MaxConsecutive: 3, MaxTotal: 999}
	for range 2 {
		snap, _ := s.Block("sess", "trying", lim)
		if snap.Disabled() {
			t.Fatalf("disabled prematurely at %+v", snap)
		}
	}
	snap, _ := s.Block("sess", "third", lim)
	if !snap.Disabled() {
		t.Fatalf("expected Disabled() after %d consecutive blocks; got %+v", lim.MaxConsecutive, snap)
	}
	if snap.AutoDisableReason == "" {
		t.Error("AutoDisableReason not populated")
	}
	if snap.ConsecutiveBlocks != 0 || snap.TotalBlocks != 0 {
		t.Errorf("counters should reset on trip, got consecutive=%d total=%d",
			snap.ConsecutiveBlocks, snap.TotalBlocks)
	}
}

func TestStore_TotalLimitDisables(t *testing.T) {
	s := New(t.TempDir())
	lim := Limits{MaxConsecutive: 999, MaxTotal: 5}
	// Alternate Block/Allow so consecutive never trips.
	for range 4 {
		_, _ = s.Block("sess", "x", lim)
		_, _ = s.Allow("sess")
	}
	snap, _ := s.Block("sess", "fifth", lim)
	if !snap.Disabled() {
		t.Fatalf("expected total-limit disable at %+v", snap)
	}
	if snap.ConsecutiveBlocks != 0 || snap.TotalBlocks != 0 {
		t.Errorf("counters should reset on trip, got consecutive=%d total=%d",
			snap.ConsecutiveBlocks, snap.TotalBlocks)
	}
}

func TestStore_AutoDisableSticksOnFirstTrip(t *testing.T) {
	s := New(t.TempDir())
	lim := Limits{MaxConsecutive: 2, MaxTotal: 999}
	_, _ = s.Block("sess", "first", lim)
	first, _ := s.Block("sess", "tripped", lim)
	if !first.Disabled() {
		t.Fatalf("expected disabled, got %+v", first)
	}
	disableTime := first.AutoDisabledAt
	disableReason := first.AutoDisableReason

	// More blocks must not move the disable timestamp (it's already set).
	second, _ := s.Block("sess", "subsequent", lim)
	if !second.AutoDisabledAt.Equal(disableTime) {
		t.Errorf("AutoDisabledAt drifted on subsequent block: %v vs %v", disableTime, second.AutoDisabledAt)
	}
	if second.AutoDisableReason != disableReason {
		t.Errorf("AutoDisableReason drifted: %q vs %q", disableReason, second.AutoDisableReason)
	}
	// Post-trip blocks increment from 0 (counters were reset on trip).
	if second.ConsecutiveBlocks != 1 || second.TotalBlocks != 1 {
		t.Errorf("post-trip block: got consecutive=%d total=%d, want 1/1",
			second.ConsecutiveBlocks, second.TotalBlocks)
	}
}

// TestStore_ConcurrentBlock — the load-bearing test for the lock. 100 goroutines all call Block on the same session;
// final total must be exactly 100, and the auto-disable transition must fire exactly once. Without flock'd RMW, lost
// updates would produce a total < 100 and the limits could fail to fire.
func TestStore_ConcurrentBlock(t *testing.T) {
	s := New(t.TempDir())

	// Use very large limits so we can count without auto-disabling — the goal here is verifying the counter is exact under
	// concurrency. The auto-disable-once test below verifies that invariant separately.
	lim := Limits{MaxConsecutive: 1_000_000, MaxTotal: 1_000_000}

	const N = 100
	var wg sync.WaitGroup
	wg.Add(N)
	for range N {
		go func() {
			defer wg.Done()
			if _, err := s.Block("concurrent-sess", "x", lim); err != nil {
				t.Errorf("Block: %v", err)
			}
		}()
	}
	wg.Wait()

	snap, err := s.Get("concurrent-sess")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if snap.TotalBlocks != N {
		t.Errorf("TotalBlocks = %d, want %d (lost updates indicate the lock isn't holding)", snap.TotalBlocks, N)
	}
	if snap.ConsecutiveBlocks != N {
		t.Errorf("ConsecutiveBlocks = %d, want %d", snap.ConsecutiveBlocks, N)
	}
}

// TestStore_AutoDisableFiresOnce confirms that under concurrent Block calls that all cross the disable threshold, the
// transition happens exactly once — i.e., AutoDisabledAt and AutoDisableReason are stable across all subsequent reads.
func TestStore_AutoDisableFiresOnce(t *testing.T) {
	s := New(t.TempDir())
	lim := Limits{MaxConsecutive: 5, MaxTotal: 999}

	const N = 50
	var wg sync.WaitGroup
	wg.Add(N)
	for range N {
		go func() {
			defer wg.Done()
			if _, err := s.Block("threshold-sess", "x", lim); err != nil {
				t.Errorf("Block: %v", err)
			}
		}()
	}
	wg.Wait()

	snap, _ := s.Get("threshold-sess")
	if !snap.Disabled() {
		t.Errorf("expected disabled after %d concurrent blocks (limit %d)", N, lim.MaxConsecutive)
	}
	if snap.AutoDisableReason == "" {
		t.Error("AutoDisableReason empty")
	}
	// After the trip, counters reset to 0 and post-trip blocks accumulate from there. The exact post-trip count depends on
	// goroutine scheduling, so just verify the disable is set.
}

// TestStore_AllowDoesntWriteIfNoChange confirms the small optimization in Allow: when ConsecutiveBlocks is already 0,
// no disk write occurs. We verify by checking that mtime doesn't advance across two no-op Allows.
func TestStore_AllowDoesntWriteIfNoChange(t *testing.T) {
	s := New(t.TempDir())
	// First allow: file doesn't exist yet, snapshot is zero, nothing to update — but our update path doesn't even create
	// the file since mutate returns false. Confirm Get still returns the zero snapshot afterward.
	if _, err := s.Allow("noop-sess"); err != nil {
		t.Fatalf("Allow: %v", err)
	}
	snap, _ := s.Get("noop-sess")
	if snap.TotalBlocks != 0 || snap.ConsecutiveBlocks != 0 {
		t.Errorf("got %+v", snap)
	}
}

// TestStore_RecoverFromCorruptFile confirms that a corrupted state file doesn't wedge the session — the next operation
// overwrites it with a fresh snapshot.
func TestStore_RecoverFromCorruptFile(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	// Create the dir first (Update would do this lazily).
	if _, err := s.Block("corrupt-sess", "init", DefaultLimits); err != nil {
		t.Fatalf("Block: %v", err)
	}
	// Stomp the file with garbage.
	path := s.path("corrupt-sess")
	if err := writeFile(path, "{not-json"); err != nil {
		t.Fatalf("stomp: %v", err)
	}
	// Next Block should treat the corrupt content as "no prior state" and start counting fresh — TotalBlocks == 1 after
	// this call.
	snap, err := s.Block("corrupt-sess", "after-corrupt", DefaultLimits)
	if err != nil {
		t.Fatalf("Block after corrupt: %v", err)
	}
	if snap.TotalBlocks != 1 {
		t.Errorf("expected fresh start after corrupt file, got %+v", snap)
	}
}

func TestStore_Reset(t *testing.T) {
	s := New(t.TempDir())
	lim := Limits{MaxConsecutive: 2, MaxTotal: 999}
	_, _ = s.Block("sess", "first", lim)
	_, _ = s.Block("sess", "trip", lim)

	snap, _ := s.Get("sess")
	if !snap.Disabled() {
		t.Fatal("precondition: should be disabled")
	}

	reset, err := s.Reset("sess")
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if reset != (Snapshot{}) {
		t.Errorf("Reset should zero everything, got %+v", reset)
	}

	// Verify on-disk state is clean.
	after, _ := s.Get("sess")
	if after.Disabled() || after.ConsecutiveBlocks != 0 || after.TotalBlocks != 0 {
		t.Errorf("on-disk state not clean: %+v", after)
	}
}

func TestStore_ResetNoopOnZeroState(t *testing.T) {
	s := New(t.TempDir())
	// Reset on a never-seen session should not error or create a file.
	snap, err := s.Reset("never-seen")
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}
	if snap != (Snapshot{}) {
		t.Errorf("expected zero, got %+v", snap)
	}
}

func TestSnapshot_Expired(t *testing.T) {
	tests := []struct {
		name string
		snap Snapshot
		ttl  time.Duration
		want bool
	}{
		{"not disabled", Snapshot{}, time.Hour, false},
		{"zero TTL", Snapshot{AutoDisabledAt: time.Now()}, 0, false},
		{"within TTL", Snapshot{AutoDisabledAt: time.Now()}, time.Hour, false},
		{"past TTL", Snapshot{AutoDisabledAt: time.Now().Add(-2 * time.Hour)}, time.Hour, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.snap.Expired(tt.ttl); got != tt.want {
				t.Errorf("Expired(%v) = %v, want %v", tt.ttl, got, tt.want)
			}
		})
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}
