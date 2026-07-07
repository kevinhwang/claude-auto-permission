// Package toolprep is the per-tool preprocessing layer that prepares each tool call for the LLM classifier. Two jobs:
//
//   - **Skip-list gate** ([Tool.Skippable]) — decide whether the classifier should run for this tool call at all.
//     Read-only tools, in-cwd file ops, and other "no failure mode worth checking" cases skip; the decider votes silent
//     and Claude Code's own permission flow handles the call.
//   - **Input projection** ([Tool.Sanitize]) — collapse the raw tool input down to the minimum the classifier should
//     see in the prompt. File writes drop the body, MCP calls flatten to `key=value` pairs, etc.
//
// Tools are registered by name on the [Registry]; the classifier asks the registry for the [Tool] matching each hook
// event's `tool_name`.
package toolprep

import "encoding/json"

// Skippable controls whether the LLM classifier should run.
type Skippable int

const (
	// SkipNone means the classifier should run.
	SkipNone Skippable = iota

	// Skip means the classifier should not run for this call. The decider votes silent so Claude Code's normal permission
	// flow handles the call.
	Skip
)

// Input is the canonical shape passed to a [Tool].
type Input struct {
	ToolName    string
	ToolInput   json.RawMessage
	Cwd         string
	ProjectRoot string
	WorkingDirs []string
}

// Tool is the per-tool plugin contract. Implementations must be safe to call concurrently and stateless across calls.
//
//go:generate go run go.uber.org/mock/mockgen@v0.6.0 -typed -source=toolprep.go -destination=mocks/toolprep_mock.go -package=mocks
type Tool interface {
	// Skippable returns Skip + reason ("in-cwd Read", etc.) or (SkipNone, "").
	Skippable(in Input) (Skippable, string)

	// Sanitize projects the tool input down to the minimum the classifier should see.
	Sanitize(in Input) (json.RawMessage, error)
}
