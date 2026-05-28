// Package state stores per-session backstop counters for the LLM classifier. After too many consecutive or total blocks
// in a session, the orchestrator auto-disables classification and falls back to manual approval.
//
// Concurrency: each session_id maps to one file. The parent process plus N subagent processes can bump the same counter
// concurrently; every update takes an exclusive POSIX advisory file lock around the read-modify-write so increments
// aren't lost.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Limits controls when Block auto-disables classification. Zero on a field disables that check.
type Limits struct {
	MaxConsecutive int
	MaxTotal       int
}

var DefaultLimits = Limits{MaxConsecutive: 3, MaxTotal: 20}

// Snapshot is the on-disk shape.
type Snapshot struct {
	ConsecutiveBlocks int       `json:"consecutive_blocks"`
	TotalBlocks       int       `json:"total_blocks"`
	AutoDisabledAt    time.Time `json:"auto_disabled_at,omitzero"`
	AutoDisableReason string    `json:"auto_disable_reason,omitempty"`
}

// Disabled reports whether the backstop has been tripped.
func (s Snapshot) Disabled() bool { return !s.AutoDisabledAt.IsZero() }

// Expired reports whether the backstop disable has outlived the given TTL. Returns false when not disabled or TTL <= 0.
func (s Snapshot) Expired(ttl time.Duration) bool {
	if s.AutoDisabledAt.IsZero() || ttl <= 0 {
		return false
	}
	return time.Since(s.AutoDisabledAt) > ttl
}

// Store persists per-session counters as one file per session id.
type Store struct {
	dir string
}

// New returns a Store rooted at dir. The directory is created lazily.
func New(dir string) *Store {
	return &Store{dir: dir}
}

// Get returns the current snapshot for sessionID. A never-seen sessionID returns a zero snapshot and nil error.
func (s *Store) Get(sessionID string) (Snapshot, error) {
	if sessionID == "" {
		return Snapshot{}, errors.New("empty session id")
	}
	path := s.path(sessionID)
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Snapshot{}, nil
		}
		return Snapshot{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	return decode(f)
}

// Allow resets the consecutive-blocks counter. Skips the write when the counter is already zero (the common case).
func (s *Store) Allow(sessionID string) (Snapshot, error) {
	return s.update(sessionID, func(snap *Snapshot) bool {
		if snap.ConsecutiveBlocks == 0 {
			return false
		}
		snap.ConsecutiveBlocks = 0
		return true
	}, Limits{})
}

// Reset zeroes both counters and clears the auto-disable flag. Used after the backstop fires its one-shot ask, and on
// TTL expiry.
func (s *Store) Reset(sessionID string) (Snapshot, error) {
	return s.update(sessionID, func(snap *Snapshot) bool {
		if *snap == (Snapshot{}) {
			return false
		}
		*snap = Snapshot{}
		return true
	}, Limits{})
}

// Block bumps both counters and auto-disables if a limit is hit. After a Disabled() snapshot, future classifications
// should short-circuit until the session ends.
func (s *Store) Block(sessionID string, reason string, lim Limits) (Snapshot, error) {
	return s.update(sessionID, func(snap *Snapshot) bool {
		snap.ConsecutiveBlocks++
		snap.TotalBlocks++

		if snap.AutoDisabledAt.IsZero() {
			if lim.MaxConsecutive > 0 && snap.ConsecutiveBlocks >= lim.MaxConsecutive {
				snap.AutoDisabledAt = time.Now()
				snap.AutoDisableReason = fmt.Sprintf("%d consecutive blocks (limit %d): %s",
					snap.ConsecutiveBlocks, lim.MaxConsecutive, reason)
				snap.ConsecutiveBlocks = 0
				snap.TotalBlocks = 0
			} else if lim.MaxTotal > 0 && snap.TotalBlocks >= lim.MaxTotal {
				snap.AutoDisabledAt = time.Now()
				snap.AutoDisableReason = fmt.Sprintf("%d total blocks (limit %d): %s",
					snap.TotalBlocks, lim.MaxTotal, reason)
				snap.ConsecutiveBlocks = 0
				snap.TotalBlocks = 0
			}
		}
		return true
	}, lim)
}

// update is the locked read-modify-write primitive. mutate returns true if it changed the snapshot; false skips the
// write.
func (s *Store) update(sessionID string, mutate func(*Snapshot) bool, _ Limits) (Snapshot, error) {
	if sessionID == "" {
		return Snapshot{}, errors.New("empty session id")
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return Snapshot{}, fmt.Errorf("mkdir %s: %w", s.dir, err)
	}

	path := s.path(sessionID)

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return Snapshot{}, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	if err := lockFile(f); err != nil {
		return Snapshot{}, fmt.Errorf("lock %s: %w", path, err)
	}
	defer unlockFile(f)

	var snap Snapshot
	if info, statErr := f.Stat(); statErr == nil && info.Size() > 0 {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return Snapshot{}, fmt.Errorf("seek: %w", err)
		}
		snap, err = decode(f)
		if err != nil {
			// Corrupt state — start fresh rather than aborting.
			snap = Snapshot{}
		}
	}

	if !mutate(&snap) {
		return snap, nil
	}

	data, err := json.Marshal(snap)
	if err != nil {
		return Snapshot{}, fmt.Errorf("marshal: %w", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return Snapshot{}, fmt.Errorf("seek: %w", err)
	}
	if err := f.Truncate(0); err != nil {
		return Snapshot{}, fmt.Errorf("truncate: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		return Snapshot{}, fmt.Errorf("write: %w", err)
	}
	return snap, nil
}

// decode reads a Snapshot from r. Empty input returns the zero Snapshot and a nil error.
func decode(r io.Reader) (Snapshot, error) {
	var snap Snapshot
	dec := json.NewDecoder(r)
	if err := dec.Decode(&snap); err != nil {
		if errors.Is(err, io.EOF) {
			return Snapshot{}, nil
		}
		return Snapshot{}, err
	}
	return snap, nil
}

func (s *Store) path(sessionID string) string {
	// Defensively sanitize so a malformed id can't escape the directory.
	clean := filepath.Base(sessionID)
	return filepath.Join(s.dir, clean+".json")
}
