// Package orchestrator drives one PreToolUse hook invocation:
//
//  1. Run every registered [decider.Decider] in turn against the same hook input and per-call [decider.Env].
//  2. Combine their votes via [decider.Combine] — precedence `deny > ask > passthrough > allow > silent`, with the deny
//     short-circuit as the only allowed early exit so a later decider can always veto an earlier permissive vote.
//  3. Emit the wire output (or stay silent on a silent verdict).
//  4. Append the decision-log entry, with a per-decider breakdown.
package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"claude-auto-permission/internal/decider"
	"claude-auto-permission/internal/decisionlog"
	"claude-auto-permission/internal/hookio"
)

// Run drives one PreToolUse invocation: combine deciders, write wire output, append decision-log entry. Failures fall
// through to silent so the user is never blocked blind.
//
// `log` may be nil (logging disabled). `deciders` may be empty (silent → no output). The caller resolves `env` once per
// invocation; see [decider.Env].
func Run(ctx context.Context, deciders []decider.Decider, log *decisionlog.Writer, in *hookio.HookInput, env decider.Env, w writer) {
	if in == nil {
		return
	}

	results := runDeciders(ctx, deciders, in, env)
	final, reason := decider.Combine(results)

	switch final {
	case decider.DecisionAllow:
		_ = hookio.WritePreToolUseAllow(w, reason)
	case decider.DecisionAsk:
		_ = hookio.WritePreToolUseAsk(w, reason)
	case decider.DecisionDeny:
		_ = hookio.WritePreToolUseDeny(w, denyMessage(reason))
	}
	// Silent and Passthrough both → no output; Claude Code's normal flow proceeds. They differ in the decision log —
	// Silent attributes "no opinion", Passthrough attributes "opinion withheld" — but the wire-level outcome is the same.

	_ = log.Append(buildLogEntry(in, final, reason, results))
}

// runDeciders invokes deciders sequentially, short-circuiting after the first deny. Skipped deciders still appear in
// the result slice with a `"skipped: prior decider denied"` reason so the log entry's `deciders` map is always
// complete.
func runDeciders(ctx context.Context, ds []decider.Decider, in *hookio.HookInput, env decider.Env) []decider.Result {
	out := make([]decider.Result, 0, len(ds))
	denied := false
	for _, d := range ds {
		var r decider.Result
		if denied {
			r = decider.Silent("skipped: prior decider denied")
		} else {
			r = d.Decide(ctx, in, env)
		}
		r.Name = d.Name()
		out = append(out, r)
		if r.Decision == decider.DecisionDeny {
			denied = true
		}
	}
	return out
}

func buildLogEntry(in *hookio.HookInput, final decider.Decision, reason string, results []decider.Result) decisionlog.Entry {
	deciders := make(map[string]decisionlog.DeciderEntry, len(results))
	for _, r := range results {
		deciders[r.Name] = decisionlog.DeciderEntry{
			Decision:  r.Decision,
			Reason:    r.Reason,
			LatencyMs: r.LatencyMs,
			Meta:      r.Meta,
		}
	}

	var inputSha string
	if len(in.ToolInput) > 0 {
		h := sha256.Sum256(in.ToolInput)
		inputSha = hex.EncodeToString(h[:8])
	}

	return decisionlog.Entry{
		Timestamp:      time.Now(),
		SessionId:      in.SessionId,
		AgentId:        in.AgentId,
		AgentType:      in.AgentType,
		Tool:           in.ToolName,
		ToolUseId:      in.ToolUseId,
		Cwd:            in.Cwd,
		ProjectRoot:    in.ProjectRoot,
		PermissionMode: in.PermissionMode,
		InputSha:       inputSha,
		Decision:       final,
		Reason:         reason,
		Deciders:       deciders,
	}
}

func denyMessage(reason string) string {
	if reason == "" {
		return "Blocked by claude-auto-permission"
	}
	return reason
}

// writer narrows io.Writer so tests can swap in buffers without dragging the full io surface into Run's signature.
type writer interface {
	Write(p []byte) (int, error)
}
