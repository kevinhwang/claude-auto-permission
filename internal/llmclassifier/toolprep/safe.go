package toolprep

import (
	"encoding/json"
	"fmt"
	"strings"
)

// SafeTools enumerates names whose worst case is reading state or shuffling agent-internal data. The list is
// intentionally generous: a miss costs one LLM call, a false positive lets through a pure-read or agent-internal call.
var SafeTools = map[string]bool{
	// Read-only file/code intelligence.
	"Grep":       true,
	"Glob":       true,
	"LSP":        true,
	"ToolSearch": true,

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
	"Sleep":           true,

	// Team coordination / internal messaging. Teammates perform their own permission checks.
	"TeamCreate":  true,
	"TeamDelete":  true,
	"SendMessage": true,

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
	if projected, ok := compactSafeToolInput(in); ok {
		return json.Marshal(projected)
	}

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

func compactSafeToolInput(in Input) (string, bool) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(in.ToolInput, &raw); err != nil {
		return "", false
	}

	switch in.ToolName {
	case "AskUserQuestion":
		return askUserQuestionInput(raw), true
	case "TodoWrite":
		if todos, ok := raw["todos"]; ok {
			var items []any
			if err := json.Unmarshal(todos, &items); err == nil {
				return fmt.Sprintf("%d items", len(items)), true
			}
		}
	case "TaskCreate":
		return stringField(raw, "subject"), true
	case "TaskUpdate":
		parts := []string{
			stringField(raw, "taskId"),
			stringField(raw, "status"),
			stringField(raw, "subject"),
		}
		return strings.Join(nonEmpty(parts), " "), true
	case "TaskGet":
		return stringField(raw, "taskId"), true
	case "TaskOutput":
		return stringField(raw, "task_id"), true
	case "TaskStop":
		if v := stringField(raw, "task_id"); v != "" {
			return v, true
		}
		return stringField(raw, "shell_id"), true
	case "ToolSearch":
		return stringField(raw, "query"), true
	case "Sleep":
		parts := []string{
			stringField(raw, "duration"),
			stringField(raw, "reason"),
		}
		return strings.Join(nonEmpty(parts), " "), true
	case "TeamCreate":
		parts := []string{
			stringField(raw, "name"),
			stringField(raw, "description"),
			stringField(raw, "prompt"),
		}
		return strings.Join(nonEmpty(parts), " "), true
	case "TeamDelete":
		return strings.Join(nonEmpty([]string{
			stringField(raw, "team_id"),
			stringField(raw, "teamId"),
			stringField(raw, "name"),
		}), " "), true
	case "SendMessage":
		return strings.Join(nonEmpty([]string{
			stringField(raw, "recipient"),
			stringField(raw, "recipient_id"),
			stringField(raw, "recipientId"),
			stringField(raw, "message"),
		}), " "), true
	case "Grep":
		pattern := stringField(raw, "pattern")
		if path := stringField(raw, "path"); path != "" {
			return pattern + " in " + path, true
		}
		return pattern, true
	case "Glob":
		return stringField(raw, "pattern"), true
	case "ListMcpResourcesTool":
		return stringField(raw, "server"), true
	case "ReadMcpResourceTool":
		return strings.TrimSpace(stringField(raw, "server") + " " + stringField(raw, "uri")), true
	}
	return "", false
}

func askUserQuestionInput(raw map[string]json.RawMessage) string {
	var questions []struct {
		Question string `json:"question"`
	}
	_ = json.Unmarshal(raw["questions"], &questions)
	parts := make([]string, 0, len(questions))
	for _, q := range questions {
		if q.Question != "" {
			parts = append(parts, q.Question)
		}
	}
	return strings.Join(parts, " | ")
}

func stringField(raw map[string]json.RawMessage, key string) string {
	var s string
	_ = json.Unmarshal(raw[key], &s)
	return s
}

func nonEmpty(parts []string) []string {
	out := parts[:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
