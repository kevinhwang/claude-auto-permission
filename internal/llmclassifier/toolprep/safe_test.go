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
		ToolInput: json.RawMessage(`{"other":"do the thing"}`),
	}
	out, err := ev.Sanitize(in)
	if err != nil {
		t.Fatalf("Sanitize: %v", err)
	}
	if !strings.Contains(string(out), "do the thing") {
		t.Errorf("short field truncated unexpectedly: %s", out)
	}
}

func TestSanitize_CompactsReferenceSafeToolInputs(t *testing.T) {
	tests := []struct {
		name      string
		toolName  string
		toolInput string
		want      string
	}{
		{
			name:      "AskUserQuestion keeps question text",
			toolName:  "AskUserQuestion",
			toolInput: `{"questions":[{"question":"Deploy now?"},{"question":"Notify team?"}]}`,
			want:      "Deploy now? | Notify team?",
		},
		{
			name:      "TodoWrite counts items",
			toolName:  "TodoWrite",
			toolInput: `{"todos":[{"content":"a"},{"content":"b"}]}`,
			want:      "2 items",
		},
		{
			name:      "TaskUpdate joins core fields",
			toolName:  "TaskUpdate",
			toolInput: `{"taskId":"17","status":"completed","subject":"Verify"}`,
			want:      "17 completed Verify",
		},
		{
			name:      "ToolSearch keeps query",
			toolName:  "ToolSearch",
			toolInput: `{"query":"select:Read,Edit"}`,
			want:      "select:Read,Edit",
		},
		{
			name:      "Sleep keeps duration and reason",
			toolName:  "Sleep",
			toolInput: `{"duration":"5m","reason":"waiting for CI"}`,
			want:      "5m waiting for CI",
		},
		{
			name:      "SendMessage keeps recipient and message",
			toolName:  "SendMessage",
			toolInput: `{"recipient":"agent-1","message":"please inspect logs"}`,
			want:      "agent-1 please inspect logs",
		},
		{
			name:      "TeamCreate keeps name and prompt",
			toolName:  "TeamCreate",
			toolInput: `{"name":"reviewers","prompt":"check the test plan"}`,
			want:      "reviewers check the test plan",
		},
		{
			name:      "TeamDelete keeps identifier",
			toolName:  "TeamDelete",
			toolInput: `{"team_id":"team-123"}`,
			want:      "team-123",
		},
		{
			name:      "Grep includes path",
			toolName:  "Grep",
			toolInput: `{"pattern":"TODO","path":"internal"}`,
			want:      "TODO in internal",
		},
		{
			name:      "ReadMcpResourceTool includes server and uri",
			toolName:  "ReadMcpResourceTool",
			toolInput: `{"server":"docs","uri":"file://x"}`,
			want:      "docs file://x",
		},
	}

	ev := NewSafe()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := ev.Sanitize(Input{
				ToolName:  tt.toolName,
				ToolInput: json.RawMessage(tt.toolInput),
			})
			if err != nil {
				t.Fatalf("Sanitize: %v", err)
			}
			var got string
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("decode: %v\nraw: %s", err, out)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}
