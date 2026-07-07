package toolprep

import (
	"encoding/json"
	"strings"
)

// Plugins for tools that reach outside the agent's local context — network (WebFetch, WebSearch) and delegation (Agent
// / Task). All return SkipNone: these are the highest-leverage calls and the classifier should look at every one.
// Free-form payloads stay verbatim — they're the field that determines whether the call is safe.

// WebFetch is the per-tool plugin for `WebFetch` — request a URL with a guidance prompt for what to extract.
type WebFetch struct{}

// NewWebFetch returns a stateless WebFetch plugin.
func NewWebFetch() WebFetch { return WebFetch{} }

func (WebFetch) Skippable(Input) (Skippable, string) {
	return SkipNone, ""
}

func (WebFetch) Sanitize(in Input) (json.RawMessage, error) {
	var input struct {
		Url    string `json:"url"`
		Prompt string `json:"prompt"`
	}
	_ = json.Unmarshal(in.ToolInput, &input)
	if input.Prompt == "" {
		return json.Marshal(input.Url)
	}
	return json.Marshal(input.Url + ": " + input.Prompt)
}

// WebSearch is the per-tool plugin for `WebSearch` — issue a web search query.
type WebSearch struct{}

// NewWebSearch returns a stateless WebSearch plugin.
func NewWebSearch() WebSearch { return WebSearch{} }

func (WebSearch) Skippable(Input) (Skippable, string) {
	return SkipNone, ""
}

func (WebSearch) Sanitize(in Input) (json.RawMessage, error) {
	var input struct {
		Query string `json:"query"`
	}
	_ = json.Unmarshal(in.ToolInput, &input)
	return json.Marshal(input.Query)
}

// Agent (alias: `Task`) is the per-tool plugin for the subagent-spawn tool. Sanitize emits
// `(subagent_type, mode=…): prompt`; the prompt stays whole — it's exactly the surface a prompt-injected agent would
// use to smuggle instructions into a fresh context.
//
// This is one of two subagent classifier checkpoints; the other is per-tool (PreToolUse fires inside the subagent for
// each tool it uses).
type Agent struct{}

// NewAgent returns a stateless Agent plugin.
func NewAgent() Agent { return Agent{} }

func (Agent) Skippable(Input) (Skippable, string) {
	return SkipNone, ""
}

func (Agent) Sanitize(in Input) (json.RawMessage, error) {
	var input struct {
		SubagentType string `json:"subagent_type"`
		Mode         string `json:"mode,omitempty"`
		Prompt       string `json:"prompt"`
	}
	_ = json.Unmarshal(in.ToolInput, &input)

	var tags []string
	if input.SubagentType != "" {
		tags = append(tags, input.SubagentType)
	}
	if input.Mode != "" {
		tags = append(tags, "mode="+input.Mode)
	}
	prefix := ": "
	if len(tags) > 0 {
		prefix = "(" + strings.Join(tags, ", ") + "): "
	}
	return json.Marshal(prefix + input.Prompt)
}
