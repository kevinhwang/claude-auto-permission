package toolprep

import (
	"encoding/json"
	"strings"
	"testing"
)

// Sanitize emits only the command string. The model-authored `description` field is dropped on purpose — see
// [Evaluator.Sanitize].
func TestSanitize_KeepsCommandDropsDescription(t *testing.T) {
	in := Input{
		ToolInput: json.RawMessage(`{"command":"ls -la","description":"List files"}`),
	}
	out, err := NewBash().Sanitize(in)
	if err != nil {
		t.Fatalf("Sanitize: %v", err)
	}
	var got string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode: %v\nraw: %s", err, out)
	}
	if got != "ls -la" {
		t.Errorf("got %q, want %q", got, "ls -la")
	}
	if strings.Contains(string(out), "List files") {
		t.Errorf("description leaked into sanitized output: %s", out)
	}
}

// Long commands pass through verbatim — volume is the transcript layer's problem, not the proposed action's.
func TestSanitize_NoTruncation(t *testing.T) {
	huge := strings.Repeat("a", 10_000)
	in := Input{
		ToolInput: json.RawMessage(`{"command":` + jsonString(huge) + `}`),
	}
	out, _ := NewBash().Sanitize(in)
	var got string
	_ = json.Unmarshal(out, &got)
	if got != huge {
		t.Errorf("expected verbatim 10k-char command, got %d chars", len(got))
	}
}

// Malformed input collapses to an empty command rather than erroring; the orchestrator's safeSanitize would otherwise
// fall back to the raw bytes anyway.
func TestSanitize_MalformedInputYieldsEmpty(t *testing.T) {
	in := Input{ToolInput: json.RawMessage(`not json`)}
	out, err := NewBash().Sanitize(in)
	if err != nil {
		t.Fatalf("Sanitize: %v", err)
	}
	if string(out) != `""` {
		t.Errorf("got %s, want empty string", out)
	}
}
