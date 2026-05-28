// Package hookio holds the JSON shapes Claude Code uses to communicate with permission hooks, plus helpers for
// stdin/stdout I/O.
//
// This tool registers for one event: PreToolUse.
package hookio

import (
	"encoding/json"
	"io"
)

const (
	EventPreToolUse = "PreToolUse"
)

// HookInput is the union of fields we read from the PreToolUse event. ToolInput stays as RawMessage so per-tool plugins
// re-decode what they need and the verbatim bytes flow into the classifier prompt.
type HookInput struct {
	// Common to all hook events
	HookEventName  string `json:"hook_event_name"`
	SessionId      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	Cwd            string `json:"cwd"`
	PermissionMode string `json:"permission_mode,omitempty"`

	// Subagent context.
	AgentId   string `json:"agent_id,omitempty"`
	AgentType string `json:"agent_type,omitempty"`

	// Tool fields
	ToolName  string          `json:"tool_name,omitempty"`
	ToolInput json.RawMessage `json:"tool_input,omitempty"`

	// PreToolUse-specific
	ToolUseId string `json:"tool_use_id,omitempty"`

	// ProjectRoot is the stable project root ($CLAUDE_PROJECT_DIR), resolved by app.Run and threaded through deciders. Not
	// on the wire.
	ProjectRoot string `json:"-"`
}

// PreToolUseOutput is the JSON envelope written to stdout.
type PreToolUseOutput struct {
	HookSpecificOutput PreToolUsePayload `json:"hookSpecificOutput"`
}

// PreToolUsePayload carries the verdict. PermissionDecision is "allow" | "deny" | "ask"; reason is the matched rule
// (allow) or deny reason.
type PreToolUsePayload struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision,omitempty"`
	PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
}

// Read decodes a HookInput from r. Errors are treated as silent fall-through by callers.
func Read(r io.Reader) (*HookInput, error) {
	var input HookInput
	if err := json.NewDecoder(r).Decode(&input); err != nil {
		return nil, err
	}
	return &input, nil
}

// WritePreToolUseAllow emits an "allow" decision with the optional matched-rule reason. Claude Code still enforces
// permissions.deny/ask on top per its docs, so a hook allow doesn't bypass configured rules.
func WritePreToolUseAllow(w io.Writer, reason string) error {
	return json.NewEncoder(w).Encode(PreToolUseOutput{
		HookSpecificOutput: PreToolUsePayload{
			HookEventName:            EventPreToolUse,
			PermissionDecision:       "allow",
			PermissionDecisionReason: reason,
		},
	})
}

// WritePreToolUseDeny emits a "deny" decision with reason.
func WritePreToolUseDeny(w io.Writer, reason string) error {
	return json.NewEncoder(w).Encode(PreToolUseOutput{
		HookSpecificOutput: PreToolUsePayload{
			HookEventName:            EventPreToolUse,
			PermissionDecision:       "deny",
			PermissionDecisionReason: reason,
		},
	})
}

// WritePreToolUseAsk emits an "ask" decision, forcing Claude Code to show the interactive permission prompt regardless
// of other permission logic (e.g. auto-mode classifier, allowlists).
func WritePreToolUseAsk(w io.Writer, reason string) error {
	return json.NewEncoder(w).Encode(PreToolUseOutput{
		HookSpecificOutput: PreToolUsePayload{
			HookEventName:            EventPreToolUse,
			PermissionDecision:       "ask",
			PermissionDecisionReason: reason,
		},
	})
}
