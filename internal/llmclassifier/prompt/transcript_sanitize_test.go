package prompt

import (
	"encoding/json"
	"strings"
	"testing"

	cctranscript "claude-auto-permission/internal/claudecode/transcript"
)

// makeEntry builds a one-line JSONL transcript entry for tests. Anything not provided in fields is zero-valued.
func makeEntry(t *testing.T, raw string) cctranscript.Entry {
	t.Helper()
	var r cctranscript.RawEntry
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatalf("decode test entry: %v\nraw: %s", err, raw)
	}
	return cctranscript.Entry{Raw: r}
}

func TestSanitize_RealUserPrompt(t *testing.T) {
	entries := []cctranscript.Entry{
		makeEntry(t, `{
			"type": "user",
			"uuid": "u1",
			"parentUuid": null,
			"message": {"role": "user", "content": "fix the bug in foo.go"}
		}`),
	}
	got, err := Sanitize(entries, nil)
	if err != nil {
		t.Fatalf("Sanitize: %v", err)
	}
	if len(got) != 1 || got[0].Kind != KindUser || got[0].User != "fix the bug in foo.go" {
		t.Errorf("got %+v", got)
	}
	if got[0].Uuid != "u1" {
		t.Errorf("Uuid = %q, want u1", got[0].Uuid)
	}
}

// Multi-turn user prompts (parentUuid set) must also reach the classifier — late-session boundary statements like
// "don't push to main" only appear on turns ≥2.
func TestSanitize_MultiTurnUserPromptsKept(t *testing.T) {
	entries := []cctranscript.Entry{
		makeEntry(t, `{
			"type": "user",
			"uuid": "u1",
			"parentUuid": null,
			"message": {"role": "user", "content": "first turn"}
		}`),
		makeEntry(t, `{
			"type": "assistant",
			"uuid": "a1",
			"parentUuid": "u1",
			"message": {"role": "assistant", "content": [
				{"type": "tool_use", "name": "Bash", "input": {"command": "ls"}}
			]}
		}`),
		makeEntry(t, `{
			"type": "user",
			"uuid": "u2",
			"parentUuid": "a1",
			"message": {"role": "user", "content": "don't push to main"}
		}`),
	}
	got, _ := Sanitize(entries, nil)
	// Expect three records: KindUser, KindCall, KindUser.
	if len(got) != 3 {
		t.Fatalf("got %d records, want 3: %+v", len(got), got)
	}
	if got[2].Kind != KindUser || got[2].User != "don't push to main" {
		t.Errorf("third record = %+v, want second user prompt", got[2])
	}
	if got[2].Uuid != "u2" {
		t.Errorf("third record Uuid = %q, want u2", got[2].Uuid)
	}
}

func TestSanitize_IsMetaUserDropped(t *testing.T) {
	entries := []cctranscript.Entry{
		makeEntry(t, `{
			"type": "user",
			"uuid": "u1",
			"isMeta": true,
			"message": {"role": "user", "content": "Continue from where you left off."}
		}`),
	}
	got, _ := Sanitize(entries, nil)
	if len(got) != 0 {
		t.Errorf("isMeta user should be dropped, got %+v", got)
	}
}

func TestSanitize_AssistantBlocks(t *testing.T) {
	tests := []struct {
		name     string
		entry    string
		wantLen  int
		wantKind RecordKind
	}{
		{
			name: "thinking dropped",
			entry: `{
				"type": "assistant",
				"uuid": "a1",
				"message": {"role": "assistant", "content": [{"type": "thinking", "thinking": "let me think..."}]}
			}`,
			wantLen: 0,
		},
		{
			name: "text dropped",
			entry: `{
				"type": "assistant",
				"uuid": "a2",
				"message": {"role": "assistant", "content": [{"type": "text", "text": "I'll read the file."}]}
			}`,
			wantLen: 0,
		},
		{
			name: "tool_use kept",
			entry: `{
				"type": "assistant",
				"uuid": "a3",
				"message": {"role": "assistant", "content": [
					{"type": "thinking", "thinking": "STRIP ME"},
					{"type": "text", "text": "STRIP ME TOO"},
					{"type": "tool_use", "name": "Bash", "input": {"command": "ls"}}
				]}
			}`,
			wantLen:  1,
			wantKind: KindCall,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := Sanitize([]cctranscript.Entry{makeEntry(t, tt.entry)}, nil)
			if len(got) != tt.wantLen {
				t.Fatalf("len = %d, want %d. got: %+v", len(got), tt.wantLen, got)
			}
			if tt.wantLen > 0 && got[0].Kind != tt.wantKind {
				t.Errorf("kind = %s, want %s", got[0].Kind, tt.wantKind)
			}
			// Confirm thinking/text content didn't leak through.
			for _, r := range got {
				blob, _ := json.Marshal(r)
				if strings.Contains(string(blob), "STRIP ME") {
					t.Errorf("stripped content leaked: %s", blob)
				}
			}
		})
	}
}

func TestSanitize_ToolResultsDropped(t *testing.T) {
	// Synthetic-user tool_result entries — both ok and is_error shapes — produce no records. The reference impl emits
	// records only for tool_use and user-text blocks; we mirror that.
	entries := []cctranscript.Entry{
		// Real user prompt (sets up the session).
		makeEntry(t, `{
			"type": "user",
			"uuid": "u1",
			"parentUuid": null,
			"message": {"role": "user", "content": "list files"}
		}`),
		// Assistant calls Bash.
		makeEntry(t, `{
			"type": "assistant",
			"uuid": "a1",
			"parentUuid": "u1",
			"message": {"role": "assistant", "content": [
				{"type": "tool_use", "name": "Bash", "input": {"command": "ls"}}
			]}
		}`),
		// Synthetic-user tool_result (success).
		makeEntry(t, `{
			"type": "user",
			"uuid": "u2",
			"parentUuid": "a1",
			"sourceToolAssistantUUID": "a1",
			"message": {"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "t1", "content": "FILE OUTPUT THAT MUST BE DROPPED"}
			]}
		}`),
		// Another tool_use + tool_result(is_error) pair.
		makeEntry(t, `{
			"type": "assistant",
			"uuid": "a2",
			"message": {"role": "assistant", "content": [
				{"type": "tool_use", "name": "Bash", "input": {"command": "false"}}
			]}
		}`),
		makeEntry(t, `{
			"type": "user",
			"uuid": "u3",
			"parentUuid": "a2",
			"sourceToolAssistantUUID": "a2",
			"message": {"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "t2", "content": "exit 1", "is_error": true}
			]}
		}`),
	}

	got, err := Sanitize(entries, nil)
	if err != nil {
		t.Fatalf("Sanitize: %v", err)
	}

	// Expect: user, call, call. tool_result entries produce no records.
	wantKinds := []RecordKind{KindUser, KindCall, KindCall}
	if len(got) != len(wantKinds) {
		t.Fatalf("got %d records, want %d: %+v", len(got), len(wantKinds), got)
	}
	for i, k := range wantKinds {
		if got[i].Kind != k {
			t.Errorf("record %d kind = %s, want %s", i, got[i].Kind, k)
		}
	}

	// Confirm tool output never leaks.
	for _, r := range got {
		blob, _ := json.Marshal(r)
		if strings.Contains(string(blob), "FILE OUTPUT") {
			t.Errorf("tool output leaked: %s", blob)
		}
	}
}

func TestSanitize_FrameworkTypesDropped(t *testing.T) {
	for _, ty := range []string{"system", "attachment", "queue-operation", "custom-title", "last-prompt", "ai-title", "file-history-snapshot"} {
		t.Run(ty, func(t *testing.T) {
			entries := []cctranscript.Entry{
				makeEntry(t, `{"type": "`+ty+`", "uuid": "x"}`),
			}
			got, _ := Sanitize(entries, nil)
			if len(got) != 0 {
				t.Errorf("%s should be dropped, got %+v", ty, got)
			}
		})
	}
}

func TestSanitize_PerToolSanitizer(t *testing.T) {
	called := 0
	redactor := func(toolName string, raw json.RawMessage) (json.RawMessage, error) {
		called++
		if toolName != "Bash" {
			t.Errorf("unexpected tool: %s", toolName)
		}
		return json.RawMessage(`{"command":"<redacted>"}`), nil
	}
	entries := []cctranscript.Entry{
		makeEntry(t, `{
			"type": "assistant",
			"uuid": "a1",
			"message": {"role": "assistant", "content": [
				{"type": "tool_use", "name": "Bash", "input": {"command": "echo SECRET"}}
			]}
		}`),
	}
	got, _ := Sanitize(entries, redactor)
	if called != 1 {
		t.Errorf("sanitizer called %d times, want 1", called)
	}
	if len(got) != 1 || !strings.Contains(string(got[0].Call.ToolInput), "<redacted>") {
		t.Errorf("got %+v", got)
	}
}

func TestSanitize_SubagentTagging(t *testing.T) {
	parentEntry := cctranscript.Entry{
		Raw: cctranscript.RawEntry{
			Type: "assistant",
			Uuid: "ap",
			Message: cctranscript.RawMessage{
				Role:    "assistant",
				Content: json.RawMessage(`[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]`),
			},
		},
		Source: cctranscript.Source{Path: "/p", SubagentType: ""},
	}
	subagentEntry := cctranscript.Entry{
		Raw: cctranscript.RawEntry{
			Type: "assistant",
			Uuid: "as",
			Message: cctranscript.RawMessage{
				Role:    "assistant",
				Content: json.RawMessage(`[{"type":"tool_use","name":"Read","input":{"file_path":"/x"}}]`),
			},
		},
		Source: cctranscript.Source{Path: "/s", SubagentType: "general-purpose"},
	}
	got, _ := Sanitize([]cctranscript.Entry{parentEntry, subagentEntry}, nil)
	if len(got) != 2 {
		t.Fatalf("got %d records", len(got))
	}
	if got[0].Subagent != "" {
		t.Errorf("parent record subagent = %q, want empty", got[0].Subagent)
	}
	if got[1].Subagent != "general-purpose" {
		t.Errorf("subagent record subagent = %q, want general-purpose", got[1].Subagent)
	}
}
