package toolprep

import (
	"encoding/json"
	"strings"
	"testing"
)

// ─── WebFetch ──────────────────────────────────────────────────────

func TestWebFetch_NeverSkippable(t *testing.T) {
	if got, _ := NewWebFetch().Skippable(Input{}); got != SkipNone {
		t.Error("WebFetch should never be skippable")
	}
}

// WebFetch sanitizes to `url: prompt` (or just url) so the classifier sees both the destination and the guidance
// string. The guidance string can carry instructions for the fetched page's interpreter and is part of what makes a
// fetch deny-worthy.
func TestWebFetch_SanitizeKeepsURLAndPrompt(t *testing.T) {
	ev := NewWebFetch()
	in := Input{
		ToolInput: json.RawMessage(`{"url":"https://example.com","prompt":"summarize"}`),
	}
	out, _ := ev.Sanitize(in)
	var got string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode: %v\nraw: %s", err, out)
	}
	if got != "https://example.com: summarize" {
		t.Errorf("got %q", got)
	}
}

func TestWebFetch_SanitizeOmitsEmptyPrompt(t *testing.T) {
	ev := NewWebFetch()
	in := Input{
		ToolInput: json.RawMessage(`{"url":"https://example.com"}`),
	}
	out, _ := ev.Sanitize(in)
	var got string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode: %v\nraw: %s", err, out)
	}
	if got != "https://example.com" {
		t.Errorf("got %q, want bare URL", got)
	}
}

// ─── WebSearch ─────────────────────────────────────────────────────

func TestWebSearch_SanitizeKeepsQuery(t *testing.T) {
	ev := NewWebSearch()
	in := Input{
		ToolInput: json.RawMessage(`{"query":"go testing","allowed_domains":["pkg.go.dev"]}`),
	}
	out, _ := ev.Sanitize(in)
	var got string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode: %v\nraw: %s", err, out)
	}
	if got != "go testing" {
		t.Errorf("got %q", got)
	}
}

// ─── Agent ─────────────────────────────────────────────────────────

// Agent sanitizes to `(subagent_type, mode=...): prompt`. The prompt body is preserved because subagent prompts are
// exactly the surface a prompt-injected parent agent would use to smuggle malicious instructions into a fresh
// classifier-blind context.
func TestAgent_SanitizeKeepsPromptBody(t *testing.T) {
	ev := NewAgent()
	prompt := "Do a thing"
	in := Input{
		ToolInput: json.RawMessage(`{"description":"investigate bug","subagent_type":"general-purpose","prompt":` + jsonString(prompt) + `}`),
	}
	out, err := ev.Sanitize(in)
	if err != nil {
		t.Fatalf("Sanitize: %v", err)
	}
	var got string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode: %v\nraw: %s", err, out)
	}
	if got != "(general-purpose): "+prompt {
		t.Errorf("got %q", got)
	}
}

func TestAgent_SanitizeIncludesMode(t *testing.T) {
	ev := NewAgent()
	in := Input{
		ToolInput: json.RawMessage(`{"description":"x","subagent_type":"Plan","mode":"plan","prompt":"do work"}`),
	}
	out, _ := ev.Sanitize(in)
	var got string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode: %v\nraw: %s", err, out)
	}
	if !strings.Contains(got, "Plan") || !strings.Contains(got, "mode=plan") {
		t.Errorf("missing tag: %q", got)
	}
}

func TestAgent_SanitizeWithoutTags(t *testing.T) {
	ev := NewAgent()
	in := Input{
		ToolInput: json.RawMessage(`{"prompt":"work"}`),
	}
	out, _ := ev.Sanitize(in)
	var got string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode: %v\nraw: %s", err, out)
	}
	if got != ": work" {
		t.Errorf("got %q, want \": work\"", got)
	}
}
