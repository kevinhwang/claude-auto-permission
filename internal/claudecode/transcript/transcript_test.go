package transcript

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLocatePath_ReturnsTranscriptPath(t *testing.T) {
	got := LocatePath("/explicit/path.jsonl")
	if got != "/explicit/path.jsonl" {
		t.Errorf("got %q", got)
	}
}

// Claude Code's hook docs document transcript_path as a common field present on every event; we trust the contract and
// return empty when it's empty, letting the classifier proceed with no transcript history.
func TestLocatePath_EmptyWhenUnset(t *testing.T) {
	if got := LocatePath(""); got != "" {
		t.Errorf("expected empty path, got %q", got)
	}
}

func TestSubagentTranscriptPath(t *testing.T) {
	parent := "/home/user/.claude/projects/-foo/parent-session.jsonl"
	got := SubagentTranscriptPath(parent, "agent-42")
	want := "/home/user/.claude/projects/-foo/parent-session/subagents/agent-agent-42.jsonl"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestReadAll_ParsesValidJSONL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	content := `{"type":"user","uuid":"u1","parentUuid":null,"message":{"role":"user","content":"hi"}}
{"type":"assistant","uuid":"a1","message":{"role":"assistant","content":[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	entries, err := ReadAll(Source{Path: path})
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("got %d entries, want 2", len(entries))
	}
	if entries[0].Raw.Type != "user" || entries[1].Raw.Type != "assistant" {
		t.Errorf("entries: %+v", entries)
	}
}

func TestReadAll_TolerantOfMalformedLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	content := `{"type":"user","uuid":"u1","message":{"role":"user","content":"hi"}}
{not valid json
{"type":"assistant","uuid":"a1","message":{"role":"assistant","content":[]}}
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	entries, err := ReadAll(Source{Path: path})
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("got %d entries, want 2 (malformed line should be skipped)", len(entries))
	}
}

func TestReadAll_MissingFile(t *testing.T) {
	_, err := ReadAll(Source{Path: "/nonexistent/transcript.jsonl"})
	if err == nil {
		t.Error("expected error for missing file")
	}
}
