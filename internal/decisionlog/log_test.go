package decisionlog

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"claude-auto-permission/internal/decider"
)

// fixture builds a typical Entry with both deciders represented — the shape the orchestrator emits for a real tool
// call.
func fixture(tool string, final decider.Decision) Entry {
	return Entry{
		Tool:     tool,
		Decision: final,
		Reason:   "test",
		Deciders: map[string]DeciderEntry{
			"static_bash_rules": {Decision: decider.DecisionAllow, Reason: "git_status_allowlist", LatencyMs: 1},
			"llm_classifier":    {Decision: final, Reason: "test", LatencyMs: 1500, Meta: map[string]string{"provider": "bedrock", "model": "haiku"}},
		},
	}
}

func TestWriter_NilIsNoOp(t *testing.T) {
	var w *Writer
	if err := w.Append(fixture("Bash", decider.DecisionAllow)); err != nil {
		t.Errorf("nil Append should be no-op, got %v", err)
	}
}

func TestWriter_NewWithEmptyPathReturnsNil(t *testing.T) {
	w := New("", 0)
	if w != nil {
		t.Errorf("expected nil writer for empty path, got %v", w)
	}
}

func TestAppend_WritesValidJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.log")
	w := New(path, 0)

	entries := []Entry{
		fixture("Bash", decider.DecisionAllow),
		fixture("Read", decider.DecisionDeny),
		fixture("Grep", decider.DecisionSilent),
	}
	for _, e := range entries {
		if err := w.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	var got []Entry
	for scanner.Scan() {
		var e Entry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			t.Errorf("malformed line: %v\nraw: %s", err, scanner.Text())
		}
		got = append(got, e)
	}
	if len(got) != len(entries) {
		t.Errorf("got %d entries, want %d", len(got), len(entries))
	}
	if got[0].Decision != decider.DecisionAllow ||
		got[1].Decision != decider.DecisionDeny ||
		got[2].Decision != decider.DecisionSilent {
		t.Errorf("entries decoded wrong: %+v", got)
	}
	// The deciders map must round-trip including its nested meta.
	if got[0].Deciders["llm_classifier"].Meta["provider"] != "bedrock" {
		t.Errorf("deciders meta lost in round-trip: %+v", got[0].Deciders)
	}
}

func TestAppend_PopulatesTimestampWhenZero(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.log")
	w := New(path, 0)
	if err := w.Append(fixture("Bash", decider.DecisionAllow)); err != nil {
		t.Fatalf("Append: %v", err)
	}
	data, _ := os.ReadFile(path)
	var e Entry
	if err := json.Unmarshal(data, &e); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if e.Timestamp.IsZero() {
		t.Error("Timestamp should be auto-populated")
	}
	if time.Since(e.Timestamp) > time.Second {
		t.Errorf("Timestamp too old: %v", e.Timestamp)
	}
}

// TestAppend_ConcurrentWriters confirms O_APPEND atomicity holds for our entry size — N goroutines each write entries,
// and the resulting file has every line intact (no interleaved corruption). Kernel guarantees this for writes under
// PIPE_BUF (4 KiB on Linux/macOS); our entries are well under that.
func TestAppend_ConcurrentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.log")
	w := New(path, 0)

	const goroutines = 20
	const perGoroutine = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := range goroutines {
		go func() {
			defer wg.Done()
			for range perGoroutine {
				e := fixture("Bash", decider.DecisionAllow)
				e.SessionId = string(rune('A' + g))
				_ = w.Append(e)
			}
		}()
	}
	wg.Wait()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != goroutines*perGoroutine {
		t.Errorf("got %d lines, want %d", len(lines), goroutines*perGoroutine)
	}
	for i, line := range lines {
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Errorf("line %d malformed: %v\nraw: %s", i, err, line)
		}
	}
}

func TestAppend_TruncatesAtSizeCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.log")
	// One entry is ~300 bytes; 1000-byte cap triggers truncation after ~3 entries and keeps the last ~3 entries as tail.
	const cap = 1000
	w := New(path, cap)
	for range 10 {
		_ = w.Append(fixture("Bash", decider.DecisionAllow))
	}
	// No rotated .1 file — truncation keeps a single file.
	if _, err := os.Stat(path + ".1"); err == nil {
		t.Error("unexpected .1 file; tail-truncation should not rotate")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	// File should be at most cap + one entry size (the append after the last truncation).
	if int64(len(data)) > cap*2 {
		t.Errorf("log size %d unreasonably large for cap %d", len(data), cap)
	}
	// Every remaining line must be valid JSON.
	for line := range strings.SplitSeq(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Errorf("malformed line after truncation: %v\nraw: %s", err, line)
		}
	}
}

func TestMaybeTruncate_FlockExclusion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.log.jsonl")
	w := New(path, 1)

	if err := w.Append(fixture("Bash", decider.DecisionAllow)); err != nil {
		t.Fatalf("seed Append: %v", err)
	}
	origData, _ := os.ReadFile(path)

	holder, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("open holder: %v", err)
	}
	defer holder.Close()
	if err := unix.Flock(int(holder.Fd()), unix.LOCK_EX); err != nil {
		t.Fatalf("flock holder: %v", err)
	}
	defer func() { _ = unix.Flock(int(holder.Fd()), unix.LOCK_UN) }()

	done := make(chan struct{})
	go func() {
		defer close(done)
		w.maybeTruncate()
	}()
	<-done

	afterData, _ := os.ReadFile(path)
	if len(afterData) != len(origData) {
		t.Errorf("truncation fired despite flock held; size changed from %d to %d", len(origData), len(afterData))
	}
}

func TestAppend_TruncationSurvivesConcurrentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "decisions.log.jsonl")
	const sizeCap = 1024
	w := New(path, sizeCap)

	const goroutines = 16
	const perGoroutine = 200

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := range goroutines {
		go func() {
			defer wg.Done()
			for range perGoroutine {
				e := fixture("Bash", decider.DecisionAllow)
				e.SessionId = string(rune('A' + g))
				_ = w.Append(e)
			}
		}()
	}
	wg.Wait()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if len(data) == 0 {
		t.Fatal("log file empty after concurrent writes")
	}
	for line := range strings.SplitSeq(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Errorf("malformed line: %v\nraw: %s", err, line)
		}
	}
}
