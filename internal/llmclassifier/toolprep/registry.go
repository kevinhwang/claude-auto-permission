package toolprep

import (
	"encoding/json"
)

// Registry maps tool names to per-tool [Tool] plugins. Production wiring lives in [NewDefaultRegistry]; tests can build
// a custom registry to swap individual plugins.
type Registry struct {
	byName map[string]Tool

	// mcpFallback handles MCP tool names by prefix match — the MCP namespace is open-ended, one entry per remote MCP tool.
	mcpFallback Tool
	mcpMatch    func(name string) bool

	// safeFallback covers the safe-tool allowlist (Grep, Glob, …).
	safeFallback Tool
	safeMatch    func(name string) bool

	// fallback handles unknown tools; defaults to [passthrough] (SkipNone, so the classifier still weighs in).
	fallback Tool
}

// NewRegistry returns a Registry with only the catch-all fallback.
func NewRegistry() *Registry {
	return &Registry{
		byName:   map[string]Tool{},
		fallback: passthrough{},
	}
}

// Register binds a [Tool] to a tool name, overwriting any prior binding.
func (r *Registry) Register(toolName string, t Tool) *Registry {
	r.byName[toolName] = t
	return r
}

// SetMcpFallback wires the plugin and name predicate for tools matching the MCP prefix.
func (r *Registry) SetMcpFallback(match func(string) bool, t Tool) *Registry {
	r.mcpMatch = match
	r.mcpFallback = t
	return r
}

// SetSafeFallback wires the plugin and name predicate for the safe-tool allowlist.
func (r *Registry) SetSafeFallback(match func(string) bool, t Tool) *Registry {
	r.safeMatch = match
	r.safeFallback = t
	return r
}

// Tool returns the plugin for toolName, falling back through MCP → safe → generic.
func (r *Registry) Tool(toolName string) Tool {
	if t, ok := r.byName[toolName]; ok {
		return t
	}
	if r.mcpFallback != nil && r.mcpMatch != nil && r.mcpMatch(toolName) {
		return r.mcpFallback
	}
	if r.safeFallback != nil && r.safeMatch != nil && r.safeMatch(toolName) {
		return r.safeFallback
	}
	if r.fallback != nil {
		return r.fallback
	}
	return passthrough{}
}

// passthrough handles unrecognized tool names: SkipNone so the classifier weighs in, and verbatim sanitization so the
// input isn't lost in the prompt.
type passthrough struct{}

func (passthrough) Skippable(Input) (Skippable, string) { return SkipNone, "" }
func (passthrough) Sanitize(in Input) (json.RawMessage, error) {
	if len(in.ToolInput) == 0 {
		return json.RawMessage(`{}`), nil
	}
	return in.ToolInput, nil
}

// NewDefaultRegistry returns the production registry of per-tool plugins. Bash is registered here so the classifier can
// sanitize Bash `tool_use` records in transcript history. The static-rule safety check itself runs through
// [claude-auto-permission/internal/staticbash.Evaluate], not through this registry.
func NewDefaultRegistry() *Registry {
	r := NewRegistry()
	r.Register("Bash", NewBash())
	r.Register("Read", NewRead())
	r.Register("Write", NewWrite())
	r.Register("Edit", NewEdit())
	r.Register("NotebookEdit", NewNotebookEdit())
	r.Register("WebFetch", NewWebFetch())
	r.Register("WebSearch", NewWebSearch())

	// Claude Code versions disagree on the subagent-spawn tool name; both "Agent" and "Task" appear in real transcripts.
	agent := NewAgent()
	r.Register("Agent", agent)
	r.Register("Task", agent)

	r.SetMcpFallback(IsMcpTool, NewMcp())
	r.SetSafeFallback(func(name string) bool { return SafeTools[name] }, NewSafe())
	return r
}
