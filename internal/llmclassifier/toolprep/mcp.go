package toolprep

import (
	"encoding/json"
	"sort"
	"strings"
)

// McpPrefix is the Claude Code namespace prefix for MCP-server-provided tools. Names begin with this prefix; the [Mcp]
// plugin handles every tool whose name matches.
const McpPrefix = "mcp__"

// IsMcpTool reports whether `name` is an MCP tool (matches the [McpPrefix]).
func IsMcpTool(name string) bool {
	return strings.HasPrefix(name, McpPrefix)
}

// Mcp is the per-tool plugin for tools matching [McpPrefix]. MCP tools are heterogeneous (pure reads vs. real side
// effects), so the cautious default is to classify all of them. Users can override per-tool via the classifier config's
// `always_skip_tools` list.
//
// Sanitization dumps every top-level key as `key=value`; values stay verbatim because they carry the signal the
// classifier needs (a `query` is the actual log query; an issue ID is the issue ID).
type Mcp struct{}

// NewMcp returns a stateless MCP plugin.
func NewMcp() Mcp { return Mcp{} }

// Skippable always returns SkipNone — every MCP call reaches the classifier unless the user has opted into a skip via
// config.
func (Mcp) Skippable(Input) (Skippable, string) {
	return SkipNone, ""
}

// Sanitize emits `k1=v1 k2=v2 …` with sorted keys (so the same input produces the same output — relevant to verdict
// caching). Empty or non-object inputs fall back to the tool name.
func (Mcp) Sanitize(in Input) (json.RawMessage, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(in.ToolInput, &raw); err != nil || len(raw) == 0 {
		return json.Marshal(in.ToolName)
	}
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(stringifyJSONValue(raw[k]))
	}
	return json.Marshal(b.String())
}

// stringifyJSONValue renders values for the classifier: strings unquoted, everything else as literal JSON text (so the
// model can recognize patterns like "references prod" inside arrays/objects).
func stringifyJSONValue(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if len(trimmed) == 0 {
		return ""
	}
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
	}
	return trimmed
}
