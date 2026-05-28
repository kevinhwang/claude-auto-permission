package toolprep

import (
	"encoding/json"
)

// Bash is the per-tool plugin for the Bash tool. It never skips — every Bash call must reach the classifier so a
// user-stated boundary in the transcript can veto an otherwise statically-allowed command — and it projects each
// invocation down to the raw command string.
//
// The structural-judge entry point for Bash lives in [claude-auto-permission/internal/staticbash.Evaluate], not here.
// This plugin participates only in the LLM classifier path.
type Bash struct{}

// NewBash returns a stateless Bash plugin.
func NewBash() Bash { return Bash{} }

// Skippable always returns SkipNone for Bash.
func (Bash) Skippable(Input) (Skippable, string) {
	return SkipNone, ""
}

// Sanitize keeps just the command string. The agent-authored `description` field is dropped on purpose — it's narrative
// the agent supplies, and a prompt-injected agent could use it to influence the classifier (same reasoning as stripping
// assistant prose from the transcript).
func (Bash) Sanitize(in Input) (json.RawMessage, error) {
	var input struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(in.ToolInput, &input)
	return json.Marshal(input.Command)
}
