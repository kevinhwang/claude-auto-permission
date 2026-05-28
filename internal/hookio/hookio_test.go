package hookio

import (
	"bytes"
	"encoding/json"
	"testing"
)

// TestRead_PreToolUse_RawInput confirms Read accepts the exact PreToolUse JSON shape Claude Code emits, including the
// fields PermissionRequest doesn't carry (tool_use_id) and the hook-event tag.
func TestRead_PreToolUse_RawInput(t *testing.T) {
	raw := []byte(`{
		"hook_event_name": "PreToolUse",
		"session_id": "sess_abc",
		"transcript_path": "/tmp/transcript.jsonl",
		"cwd": "/project",
		"tool_name": "Bash",
		"tool_input": {"command": "git status"},
		"tool_use_id": "toolu_01"
	}`)
	parsed, err := Read(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if parsed.HookEventName != EventPreToolUse {
		t.Errorf("HookEventName = %q, want PreToolUse", parsed.HookEventName)
	}
	if parsed.SessionId != "sess_abc" {
		t.Errorf("SessionID = %q", parsed.SessionId)
	}
	if parsed.TranscriptPath != "/tmp/transcript.jsonl" {
		t.Errorf("TranscriptPath = %q", parsed.TranscriptPath)
	}
	if parsed.ToolUseId != "toolu_01" {
		t.Errorf("ToolUseID = %q", parsed.ToolUseId)
	}
	// tool_input flows through as raw JSON; the bash evaluator decodes the {command} shape itself.
	var ti map[string]string
	if err := json.Unmarshal(parsed.ToolInput, &ti); err != nil {
		t.Fatalf("decode ToolInput: %v", err)
	}
	if ti["command"] != "git status" {
		t.Errorf("command = %q", ti["command"])
	}
}

func TestWritePreToolUseAsk(t *testing.T) {
	var buf bytes.Buffer
	if err := WritePreToolUseAsk(&buf, "backstop: 3 consecutive blocks"); err != nil {
		t.Fatalf("WritePreToolUseAsk: %v", err)
	}
	var out PreToolUseOutput
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.HookSpecificOutput.HookEventName != EventPreToolUse {
		t.Errorf("hookEventName = %q", out.HookSpecificOutput.HookEventName)
	}
	if out.HookSpecificOutput.PermissionDecision != "ask" {
		t.Errorf("permissionDecision = %q, want ask", out.HookSpecificOutput.PermissionDecision)
	}
	if out.HookSpecificOutput.PermissionDecisionReason != "backstop: 3 consecutive blocks" {
		t.Errorf("reason = %q", out.HookSpecificOutput.PermissionDecisionReason)
	}
}

// TestRead_PreToolUse_SubagentFields confirms the optional agent_id / agent_type fields parse without error and are
// preserved on the HookInput for downstream consumers (e.g., subagent-aware classifier paths).
func TestRead_PreToolUse_SubagentFields(t *testing.T) {
	raw := []byte(`{
		"hook_event_name": "PreToolUse",
		"session_id": "sess_xyz",
		"transcript_path": "/tmp/parent.jsonl",
		"cwd": "/project",
		"agent_id": "agent_42",
		"agent_type": "general-purpose",
		"tool_name": "Bash",
		"tool_input": {"command": "ls"},
		"tool_use_id": "toolu_02"
	}`)
	parsed, err := Read(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if parsed.AgentId != "agent_42" {
		t.Errorf("AgentID = %q", parsed.AgentId)
	}
	if parsed.AgentType != "general-purpose" {
		t.Errorf("AgentType = %q", parsed.AgentType)
	}
}
