package llmclassifier

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"claude-auto-permission/internal/llmclassifier/providers"
)

// maybeDump writes req/res to disk when dir is non-empty. Debug instrumentation; errors are swallowed.
func maybeDump(dir, providerName string, req providers.Request, res providers.Result, perr error) {
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	base := strconv.FormatInt(time.Now().UnixNano(), 10)
	reqBlob, _ := json.MarshalIndent(struct {
		Provider     string          `json:"provider"`
		SystemPrompt string          `json:"system_prompt"`
		UserPrefix   string          `json:"user_prefix,omitempty"`
		UserPrompt   string          `json:"user_prompt"`
		Schema       json.RawMessage `json:"schema,omitempty"`
	}{
		Provider:     providerName,
		SystemPrompt: req.SystemPrompt,
		UserPrefix:   req.UserPrefix,
		UserPrompt:   req.UserPrompt,
		Schema:       req.Schema,
	}, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, base+".req.json"), reqBlob, 0o600)

	type resOut struct {
		ShouldBlock bool   `json:"should_block"`
		Reason      string `json:"reason,omitempty"`
		LatencyMs   int    `json:"latency_ms"`
		RawResponse string `json:"raw_response,omitempty"`
		Error       string `json:"error,omitempty"`
	}
	out := resOut{
		ShouldBlock: res.ShouldBlock,
		Reason:      res.Reason,
		LatencyMs:   res.LatencyMs,
		RawResponse: string(res.RawResponse),
	}
	if perr != nil {
		out.Error = perr.Error()
	}
	resBlob, _ := json.MarshalIndent(out, "", "  ")
	_ = os.WriteFile(filepath.Join(dir, base+".res.json"), resBlob, 0o600)
}
