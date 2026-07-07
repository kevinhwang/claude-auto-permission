// Package staticbash is the structural judge: a recursive Bash AST walker plus a config-driven rule engine. It
// auto-approves Bash commands whose every component is provably safe under the per-cwd rule set, and stays silent on
// anything else.
//
// Sub-packages:
//
//   - [ast] — the AST walker.
//   - [rules] — the rule engine.
//   - [builtins] — per-command Checker implementations (sed, awk, and aliases).
//   - [cmdcheck] — the Checker contract plus shell-word helpers.
//
// The judge votes:
//
//   - Allow with the matched rule name(s) when every command component clears the per-cwd rule set.
//   - Silent on non-Bash tools, parse errors, empty commands, or unmatched commands.
//   - Deny is reserved; the walker doesn't emit it today.
//
// Bash is the only tool this judge votes Allow on; non-Bash evaluators participate only in the classifier's skip-list.
package staticbash

import (
	"context"
	"encoding/json"
	"strings"

	"claude-auto-permission/internal/config"
	"claude-auto-permission/internal/decider"
	"claude-auto-permission/internal/hookio"
	"claude-auto-permission/internal/staticbash/builtins"
	"claude-auto-permission/internal/staticbash/cmdcheck"
)

// Decider implements [decider.Decider] for the structural judge. Its stable name is [decider.NameStaticBash].
type Decider struct {
	checkers *cmdcheck.Registry
	cfg      *config.Resolver
}

// New constructs a Decider with the default checker registry (sed/awk + aliases). cfg is required.
func New(cfg *config.Resolver) *Decider {
	return &Decider{
		checkers: builtins.DefaultRegistry(),
		cfg:      cfg,
	}
}

func (*Decider) Name() string { return decider.NameStaticBash }

// Decide is pure CPU work — never reads the transcript, never makes a network call, never touches `env`. The
// orchestrator runs it before the LLM classifier so a static allow can save the round-trip cost in a future "skip on
// prior allow" mode.
func (d *Decider) Decide(_ context.Context, in *hookio.HookInput, _ decider.Env) decider.Result {
	if in == nil {
		return decider.Silent("nil hook input")
	}
	if in.ToolName != "Bash" {
		return decider.Silent("not bash")
	}

	// Surface a specific silent reason for malformed input. Evaluate folds parse-error / empty / no-match into a single
	// fall-through, so the diagnostic value is in catching it here.
	if reason, ok := earlySilent(in.ToolInput); ok {
		return decider.Silent(reason)
	}

	v := Evaluate(Input{
		ToolInput:   in.ToolInput,
		Cwd:         in.Cwd,
		ProjectRoot: in.ProjectRoot,
	}, d.checkers, d.cfg)
	if !v.Allowed {
		return decider.Silent("no static rule matched")
	}
	return decider.Allow(formatMatched(v.Matched))
}

// formatMatched renders matched rule names. A compound command can have multiple pieces match — duplicates and source
// order are preserved.
//
//	["git"]         → "matched static rule: git"
//	["cat", "grep"] → "matched static rules: cat, grep"
func formatMatched(matched []string) string {
	switch len(matched) {
	case 0:
		// Defensive: should not happen for any real rule, but the walker's contract is allowed=true ⇒ matched non-empty, not
		// an invariant we want to crash on.
		return "matched static rule"
	case 1:
		return "matched static rule: " + matched[0]
	default:
		return "matched static rules: " + strings.Join(matched, ", ")
	}
}

// earlySilent extracts a specific silent reason from malformed or empty input so the decision log captures it. Returns
// `(reason, true)` on a hit, `("", false)` to defer to [Evaluate].
func earlySilent(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "empty tool input", true
	}
	var ti struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(raw, &ti); err != nil {
		return "malformed tool input json", true
	}
	if ti.Command == "" {
		return "empty command", true
	}
	return "", false
}
