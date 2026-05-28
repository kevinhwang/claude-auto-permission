package toolprep

import (
	"encoding/json"
	"strings"
)

// SafeTools enumerates names whose worst case is reading state or shuffling agent-internal data. The list is
// intentionally generous: a miss costs one LLM call, a false positive lets through a pure-read or agent-internal call.
var SafeTools = map[string]bool{
	// Read-only file/code intelligence.
	"Grep": true,
	"Glob": true,
	"LSP":  true,

	// In-agent task management — modifies the agent's local todo list, no external side effects.
	"TodoWrite":  true,
	"TaskCreate": true,
	"TaskUpdate": true,
	"TaskList":   true,
	"TaskGet":    true,
	"TaskOutput": true,
	"TaskStop":   true,

	// Conversational / planning helpers — no side effects.
	"AskUserQuestion": true,
	"ExitPlanMode":    true,
	"EnterPlanMode":   true,

	// MCP discovery — read-only.
	"ListMcpResourcesTool": true,
	"ReadMcpResourceTool":  true,

	"NotebookRead": true,
}

// Safe is the per-tool plugin for tools in [SafeTools] — read-only operations, agent-internal task management,
// conversational helpers. The decider votes silent so Claude Code's normal flow handles them.
type Safe struct{}

// NewSafe returns a stateless Safe plugin.
func NewSafe() Safe { return Safe{} }

// Skippable returns Skip when `in.ToolName` is in [SafeTools].
func (Safe) Skippable(in Input) (Skippable, string) {
	if SafeTools[in.ToolName] {
		return Skip, "skipped: safe-tool allowlist"
	}
	return SkipNone, ""
}

// Sanitize bounds the prompt size when these tools do hit the classifier (e.g., a TodoWrite called with a giant todo
// list).
func (Safe) Sanitize(in Input) (json.RawMessage, error) {
	const maxField = 256 // truncation limit for tool_input field values before classification
	if len(in.ToolInput) <= maxField {
		return in.ToolInput, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(in.ToolInput, &raw); err != nil {
		// Not an object — return a placeholder so we don't leak arbitrary bytes into the classifier prompt.
		return json.RawMessage(`{}`), nil
	}
	for k, v := range raw {
		if len(v) > maxField {
			raw[k] = truncateRaw(v, maxField)
		}
	}
	return json.Marshal(raw)
}

// truncateRaw shortens a JSON value: strings keep the head, arrays/objects collapse to a summary marker. Output is
// valid JSON so the classifier prompt doesn't leak arbitrary bytes.
func truncateRaw(v json.RawMessage, max int) json.RawMessage {
	trimmed := strings.TrimSpace(string(v))
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			if len(s) > max {
				s = s[:max] + "…"
			}
			b, _ := json.Marshal(s)
			return b
		}
	}
	// Anything else (numbers, arrays, objects) collapses to a marker rather than leaking arbitrary bytes.
	return json.RawMessage(`"<truncated>"`)
}
