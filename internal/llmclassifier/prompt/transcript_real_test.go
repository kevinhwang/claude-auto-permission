package prompt

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	cctranscript "claude-auto-permission/internal/claudecode/transcript"
)

// TestEndToEnd_RealTranscript reads any live session JSONL files available under `~/.claude/projects/`, runs the full
// sanitizer, and asserts high-level invariants of the projected records. This is a "does it behave sanely on real data"
// check, not a golden test — it skips when no transcripts exist (CI, fresh machine, etc.).
func TestEndToEnd_RealTranscript(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	projectsDir := filepath.Join(home, ".claude", "projects")
	if _, err := os.Stat(projectsDir); err != nil {
		t.Skip("no ~/.claude/projects directory")
	}
	matches, _ := filepath.Glob(filepath.Join(projectsDir, "*", "*.jsonl"))
	if len(matches) == 0 {
		t.Skip("no transcripts on disk")
	}

	// Transcripts are stable once written; the first match is fine.
	path := matches[0]

	entries, err := cctranscript.ReadAll(cctranscript.Source{Path: path})
	if err != nil {
		t.Fatalf("ReadAll(%s): %v", path, err)
	}
	if len(entries) == 0 {
		t.Skip("transcript empty")
	}

	records, err := Sanitize(entries, nil)
	if err != nil {
		t.Fatalf("Sanitize: %v", err)
	}

	// Invariant 1 — every record has a known kind. Resumed and compacted sessions can start without a `parentUuid==null`
	// user entry, so we don't assert that one exists; the kind check is the load-bearing assertion.
	for i, r := range records {
		switch r.Kind {
		case KindUser, KindCall:
		default:
			t.Errorf("record %d has unexpected kind %q", i, r.Kind)
		}
	}

	// Invariant 2 — every record marshals to either `{"<key>":...}` or `{"<key>":..., "_subagent":...}`. Any other shape
	// means we leaked an internal field through `MarshalJSON`.
	for i, r := range records {
		blob, err := json.Marshal(r)
		if err != nil {
			t.Errorf("record %d not JSON-encodable: %v", i, err)
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(blob, &m); err != nil {
			t.Errorf("record %d not round-trippable: %v", i, err)
			continue
		}
		if len(m) == 0 || len(m) > 2 {
			t.Errorf("record %d has %d top-level fields, want 1 or 2: %s", i, len(m), blob)
			continue
		}
		if len(m) == 2 {
			if _, ok := m["_subagent"]; !ok {
				t.Errorf("record %d has two fields but no _subagent: %s", i, blob)
			}
		}
	}
}
