package toolprep

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSkippable_SkipsListedTools(t *testing.T) {
	ev := NewSafe()
	for tool := range SafeTools {
		t.Run(tool, func(t *testing.T) {
			got, reason := ev.Skippable(Input{ToolName: tool})
			if got != Skip {
				t.Errorf("Skippable(%s) = %v, want Skip", tool, got)
			}
			if reason == "" {
				t.Errorf("Skippable(%s): expected reason text, got empty", tool)
			}
		})
	}
}

func TestSkippable_DefaultsToSkipNone(t *testing.T) {
	ev := NewSafe()
	for _, name := range []string{"Bash", "Write", "WebFetch", "mcp__atlassian__createJiraIssue", "Unknown"} {
		got, reason := ev.Skippable(Input{ToolName: name})
		if got != SkipNone {
			t.Errorf("Skippable(%s) = %v, want SkipNone", name, got)
		}
		if reason != "" {
			t.Errorf("Skippable(%s): expected empty reason for SkipNone, got %q", name, reason)
		}
	}
}

func TestSanitize_TruncatesLongFields(t *testing.T) {
	ev := NewSafe()
	huge := strings.Repeat("x", 1000)
	in := Input{
		ToolName:  "TodoWrite",
		ToolInput: json.RawMessage(`{"todos":` + jsonString(huge) + `}`),
	}
	out, err := ev.Sanitize(in)
	if err != nil {
		t.Fatalf("Sanitize: %v", err)
	}
	if len(out) > 600 {
		t.Errorf("sanitized output too long: %d bytes", len(out))
	}
	if !strings.Contains(string(out), "…") {
		t.Errorf("expected ellipsis marker; got %s", out)
	}
}

func TestSanitize_PassesShortFieldsThrough(t *testing.T) {
	ev := NewSafe()
	in := Input{
		ToolName:  "TodoWrite",
		ToolInput: json.RawMessage(`{"todos":"do the thing"}`),
	}
	out, err := ev.Sanitize(in)
	if err != nil {
		t.Fatalf("Sanitize: %v", err)
	}
	if !strings.Contains(string(out), "do the thing") {
		t.Errorf("short field truncated unexpectedly: %s", out)
	}
}
