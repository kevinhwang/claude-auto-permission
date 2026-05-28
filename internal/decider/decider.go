// Package decider defines the shared vocabulary for PreToolUse decision plugins.
//
// Each Decider casts a five-valued vote (allow / ask / passthrough / deny / silent). The orchestrator combines votes
// with `deny > ask > passthrough > allow > silent` precedence ([Combine]), short-circuiting on the first deny but never
// on the others — a later decider must be able to veto an earlier permissive vote.
//
// `passthrough` exists for the case where a decider was *supposed* to weigh in but couldn't (e.g. the LLM classifier's
// provider is down). It overrides any `allow` so the static engine doesn't auto-approve blind, but emits no wire output
// — Claude Code's normal permission flow handles the call. Cleanly distinguishable in the decision log from `silent`
// ("this decider has no opinion here").
//
// Implementations never return errors. Internal failures map to a populated `Reason` on [Silent] (no opinion) or
// [Passthrough] (opinion withheld); the decision log captures both.
package decider

import (
	"context"

	"claude-auto-permission/internal/claudecode/claudemd"
	"claude-auto-permission/internal/claudecode/permscope"
	"claude-auto-permission/internal/hookio"
)

// Decision is the five-valued vote a Decider casts.
type Decision string

const (
	DecisionAllow       Decision = "allow"
	DecisionAsk         Decision = "ask"
	DecisionPassthrough Decision = "passthrough"
	DecisionDeny        Decision = "deny"
	DecisionSilent      Decision = "silent"
)

// Env is the per-call execution environment shared across deciders. The orchestrator's caller resolves it once per hook
// invocation and hands the same value to every decider, so individual deciders don't re-derive paths/policies/CLAUDE.md
// from the file system on each call.
//
// Fields are zero-valuable; deciders that don't need a particular resolution can simply ignore it. The static-judge
// decider, for example, only reads `Cwd` and `ProjectRoot`.
type Env struct {
	// Cwd is the hook input's `cwd` (the originalCwd Claude Code was launched from at the time of the tool call).
	Cwd string

	// ProjectRoot is `$CLAUDE_PROJECT_DIR` when set, otherwise Cwd. Stable across worktree moves.
	ProjectRoot string

	// PermScope is the resolved permission scope from Claude Code's settings hierarchy:
	//
	//   - `permissions.additionalDirectories` and `permissions.deny` unioned across tiers.
	//   - The full settings-file candidate list (existing or not), surfaced via [permscope.Resolution.Candidates] for
	//     consumers that need to fingerprint the entire settings surface (e.g. the auto-mode policy cache key).
	//
	// Empty when resolution failed; downstream consumers fall back to classify rather than treat empty as "no scope".
	PermScope permscope.Resolution

	// Instructions is the resolved CLAUDE.md tree (raw — no framing preamble). Consumers add their own framing before
	// injecting it into prompts or other artifacts.
	Instructions claudemd.Bundle
}

// Decider votes on a tool call. Implementations must be safe for concurrent use across goroutines.
type Decider interface {
	// Name is a stable, log-friendly identifier (lowercase_snake_case). Values become JSON keys in the decision log;
	// changing one is a breaking change to log consumers.
	Name() string

	// Decide casts a vote on the hook input. Never returns an error: internal failures map to a [Silent] result with a
	// populated `Reason`.
	Decide(ctx context.Context, in *hookio.HookInput, env Env) Result
}

// Result is one decider's vote plus diagnostic context. `Name` is populated by the orchestrator from the decider's
// [Decider.Name] so the combiner and decision log can attribute the vote back to its source.
type Result struct {
	// Name is the decider's stable identifier. Set by the orchestrator after Decide returns; deciders themselves don't
	// touch this field.
	Name string

	Decision Decision

	// Reason is free-form diagnostic text:
	//
	//   - Allow: matched-rule name (e.g. "git_status_allowlist") or empty.
	//   - Ask: human-readable prompt context (e.g. "backstop tripped").
	//   - Passthrough: why the verdict is withheld (e.g. "bedrock timeout").
	//   - Deny: verbatim deny reason. Required.
	//   - Silent: why the decider abstained. ALWAYS populate — this is the highest-value diagnostic in the log. Examples:
	//     "not bash", "skipped: in_cwd_read", "transcript empty".
	Reason string

	// LatencyMs is the wall-clock cost of this decider's vote.
	LatencyMs int

	// Meta carries per-decider telemetry (model, provider, …) that doesn't fit in `Reason`. Surfaces under
	// `deciders.<name>.meta` in the decision log.
	Meta map[string]string
}

// Allow returns a Result voting [DecisionAllow] with the given reason (typically a matched-rule name; may be empty).
func Allow(reason string) Result {
	return Result{Decision: DecisionAllow, Reason: reason}
}

// Deny returns a Result voting [DecisionDeny]. The reason is required and surfaces in the wire output to Claude Code.
func Deny(reason string) Result {
	return Result{Decision: DecisionDeny, Reason: reason}
}

// Ask returns a Result voting [DecisionAsk], forcing Claude Code to show its interactive permission prompt regardless
// of any other layer's verdict.
func Ask(reason string) Result {
	return Result{Decision: DecisionAsk, Reason: reason}
}

// Passthrough returns a Result voting [DecisionPassthrough]: the decider had a stake in the call but its verdict is
// unavailable (transcript read failure, prompt build failure, provider outage, …). Beats Allow so a peer decider's
// permissive vote can't auto-approve while this one is incapacitated; emits no wire output so Claude Code's normal
// permission flow handles the call.
func Passthrough(reason string) Result {
	return Result{Decision: DecisionPassthrough, Reason: reason}
}

// Silent returns a Result voting [DecisionSilent], the no-opinion vote. Always populate `reason` — the decision log
// uses it to explain why this decider abstained.
func Silent(reason string) Result {
	return Result{Decision: DecisionSilent, Reason: reason}
}
