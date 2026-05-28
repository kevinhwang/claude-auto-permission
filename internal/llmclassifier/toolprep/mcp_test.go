package toolprep

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestIsMcpTool(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"mcp__atlassian__createJiraIssue", true},
		{"mcp__dropbox-devtools__query-v2", true},
		{"mcp__", true}, // technically a match; harmless
		{"Bash", false},
		{"Read", false},
		{"WebFetch", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsMcpTool(tt.name); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMcp_SkippableIsAlwaysSkipNone(t *testing.T) {
	ev := NewMcp()
	for _, name := range []string{
		"mcp__atlassian__createJiraIssue",
		"mcp__sentry__update_issue",
	} {
		if got, _ := ev.Skippable(Input{ToolName: name}); got != SkipNone {
			t.Errorf("Skippable(%s) = %v, want SkipNone", name, got)
		}
	}
}

// MCP sanitize mirrors the reference: every top-level key+value dumped as `key=value`, sorted for cache stability.
// Strings are unquoted; nested values keep their JSON form so the classifier can read literal references to prod /
// sensitive infra in the values themselves.
func TestMcp_SanitizeKeyValuePairs(t *testing.T) {
	ev := NewMcp()
	in := Input{
		ToolName: "mcp__slack__sendMessage",
		ToolInput: json.RawMessage(`{
			"channel":"C123",
			"text":"deploy starting"
		}`),
	}
	out, err := ev.Sanitize(in)
	if err != nil {
		t.Fatalf("Sanitize: %v", err)
	}
	var got string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode: %v\nraw: %s", err, out)
	}
	if got != "channel=C123 text=deploy starting" {
		t.Errorf("got %q", got)
	}
}

// Long string values are passed through verbatim. Truncation is the transcript layer's job, not per-key.
func TestMcp_SanitizePreservesLongStrings(t *testing.T) {
	ev := NewMcp()
	huge := strings.Repeat("secret ", 50)
	in := Input{
		ToolInput: json.RawMessage(`{"text":` + jsonString(huge) + `}`),
	}
	out, _ := ev.Sanitize(in)
	if !strings.Contains(string(out), "secret secret secret") {
		t.Errorf("expected long string preserved: %s", out)
	}
}

// Arrays and objects keep their JSON form — the model needs to read
// `additional_fields={"customfield_10014":"OTCPAC-1105"}` to spot deny-worthy values, not `<array len 1>`.
func TestMcp_SanitizeKeepsNestedJSON(t *testing.T) {
	ev := NewMcp()
	in := Input{
		ToolInput: json.RawMessage(`{"items":[1,2,3],"meta":{"a":1}}`),
	}
	out, err := ev.Sanitize(in)
	if err != nil {
		t.Fatalf("Sanitize: %v", err)
	}
	var got string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode: %v\nraw: %s", err, out)
	}
	if got != `items=[1,2,3] meta={"a":1}` {
		t.Errorf("got %q", got)
	}
}

func TestMcp_SanitizeScalarTypes(t *testing.T) {
	ev := NewMcp()
	in := Input{
		ToolInput: json.RawMessage(`{"num":42,"flag":true,"empty":null,"frac":3.14}`),
	}
	out, _ := ev.Sanitize(in)
	var got string
	_ = json.Unmarshal(out, &got)
	for _, want := range []string{"num=42", "flag=true", "empty=null", "frac=3.14"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

// Empty / non-object inputs collapse to the tool name — matches the reference's behavior of using the name as the
// fallback.
func TestMcp_SanitizeEmptyInputUsesToolName(t *testing.T) {
	ev := NewMcp()
	for _, raw := range []string{`{}`, `["not an object"]`, `null`, ``} {
		t.Run(raw, func(t *testing.T) {
			in := Input{
				ToolName:  "mcp__server__tool",
				ToolInput: json.RawMessage(raw),
			}
			out, err := ev.Sanitize(in)
			if err != nil {
				t.Fatalf("Sanitize: %v", err)
			}
			if string(out) != `"mcp__server__tool"` {
				t.Errorf("got %s, want \"mcp__server__tool\"", out)
			}
		})
	}
}

// Cache stability: same input → same output, regardless of map iteration order.
func TestMcp_SanitizeOrderStable(t *testing.T) {
	ev := NewMcp()
	in := Input{
		ToolInput: json.RawMessage(`{"z":1,"a":2,"m":3}`),
	}
	first, _ := ev.Sanitize(in)
	for range 50 {
		out, _ := ev.Sanitize(in)
		if string(out) != string(first) {
			t.Fatalf("non-deterministic output: %s vs %s", first, out)
		}
	}
	var s string
	_ = json.Unmarshal(first, &s)
	if s != "a=2 m=3 z=1" {
		t.Errorf("expected sorted keys, got %q", s)
	}
}
