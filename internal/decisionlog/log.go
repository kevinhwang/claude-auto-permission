// Package decisionlog writes one JSONL line per tool-call decision.
//
// Concurrency: O_APPEND is POSIX-atomic for writes under PIPE_BUF (4 KiB on Linux/macOS); decision lines stay well
// under that, so concurrent writers don't need locks for appends. Rotation does need synchronization to prevent two
// processes from both renaming, so the stat+rename pair is guarded by a non-blocking exclusive flock on the active file
// — losers skip rotation and let the holder do it.
package decisionlog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"claude-auto-permission/internal/decider"
)

// Entry is the on-disk shape of one decision line. Fields are ordered for tail -f readability: timestamp, session/tool
// identifiers, then the verdict and per-decider breakdown.
type Entry struct {
	Timestamp time.Time `json:"ts"`
	SessionId string    `json:"session_id,omitempty"`
	AgentId   string    `json:"agent_id,omitempty"`
	AgentType string    `json:"agent_type,omitempty"`

	Tool      string `json:"tool"`
	ToolUseId string `json:"tool_use_id,omitempty"`

	Cwd         string `json:"cwd,omitempty"`
	ProjectRoot string `json:"project_root,omitempty"`
	// PermissionMode is "default", "plan", "acceptEdits", or "bypassPermissions".
	PermissionMode string `json:"permission_mode,omitempty"`
	// InputSha is the first 16 hex chars of sha256(tool_input).
	InputSha string `json:"input_sha,omitempty"`

	// Decision is the combiner's verdict across all deciders.
	Decision decider.Decision `json:"decision"`
	Reason   string           `json:"reason,omitempty"`

	// Deciders always includes every registered decider — short-circuited ones record silent with reason "skipped: prior
	// decider denied".
	Deciders map[string]DeciderEntry `json:"deciders"`
}

// DeciderEntry is one decider's contribution to a tool-call decision.
type DeciderEntry struct {
	Decision  decider.Decision  `json:"decision"`
	Reason    string            `json:"reason,omitempty"`
	LatencyMs int               `json:"latency_ms,omitempty"`
	Meta      map[string]string `json:"meta,omitempty"`
}

// Writer appends Entries to a JSONL file, rotating beyond a size cap. Construct with New; nil receiver is a no-op so
// disabled writers can be passed safely.
type Writer struct {
	path     string
	maxBytes int64
}

// New returns a Writer that appends to path, rotating to <path>.1 at maxBytes. maxBytes <= 0 disables rotation. Empty
// path returns nil — Append on nil is a no-op (used when log_decisions is off).
func New(path string, maxBytes int64) *Writer {
	if path == "" {
		return nil
	}
	return &Writer{path: path, maxBytes: maxBytes}
}

// Append writes one entry as a newline-terminated JSON line.
func (w *Writer) Append(e Entry) error {
	if w == nil {
		return nil
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now()
	}
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal entry: %w", err)
	}
	data = append(data, '\n')

	if err := os.MkdirAll(filepath.Dir(w.path), 0o755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	// Truncate before opening so the appended line lands in the fresh file.
	w.maybeTruncate()

	f, err := os.OpenFile(w.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open log: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write log: %w", err)
	}
	return nil
}

// maybeTruncate keeps the last maxBytes of complete lines when the file exceeds the limit. Best-effort: any error
// short-circuits without bubbling up. Cross-process safety comes from a non-blocking exclusive flock — losers skip and
// let the holder handle it.
func (w *Writer) maybeTruncate() {
	if w.maxBytes <= 0 {
		return
	}
	f, err := os.OpenFile(w.path, os.O_RDONLY, 0)
	if err != nil {
		return
	}
	defer f.Close()

	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return
	}

	info, err := f.Stat()
	if err != nil || info.Size() <= w.maxBytes {
		return
	}

	// Read the last maxBytes of the file.
	offset := info.Size() - w.maxBytes
	buf := make([]byte, w.maxBytes)
	n, err := f.ReadAt(buf, offset)
	if err != nil && n == 0 {
		return
	}
	buf = buf[:n]

	// Advance to the first complete line boundary.
	idx := 0
	for idx < len(buf) && buf[idx] != '\n' {
		idx++
	}
	if idx < len(buf) {
		idx++ // skip the newline itself
	}
	tail := buf[idx:]
	if len(tail) == 0 {
		return
	}

	// Write tail to a temp file, then atomically replace.
	tmp := w.path + ".tmp"
	if err := os.WriteFile(tmp, tail, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, w.path)
}
