// Package ast parses bash commands and walks the resulting AST, reporting whether every component clears the configured
// rule set.
//
// Entry points:
//
//   - [Evaluate]: parse + walk + verdict in one call. Pass an [Input] describing the command, working scope, rule set,
//     and checkers; get back a [Verdict] with the safety boolean and the names of the rules that matched (source order,
//     duplicates kept).
//   - [ParseCommand]: standalone parser for callers that want the AST for inspection without running it through
//     [Evaluate].
//
// Lower-level dependencies: [rules] for the rule engine, [cmdcheck] for the Checker contract, the
// `mvdan.cc/sh/v3/syntax` parser for the AST itself.
package ast

import (
	"strings"

	"claude-auto-permission/internal/config"
	rulespb "claude-auto-permission/internal/gen/rules/v1"
	"claude-auto-permission/internal/staticbash/cmdcheck"

	"mvdan.cc/sh/v3/syntax"
)

// Input is everything the AST walker needs to render a [Verdict].
//
// Callers typically populate `Command`, `Cwd`, `ProjectRoot`, and the engine pieces (`RuleSet`, `Checkers`, `Config`).
// `WriteDirs` is non-nil only on remote-host recursion (ssh/tsh), where the writable set comes from a per-host config
// rather than the project's `allow_write_patterns`.
type Input struct {
	Command     string
	Cwd         string
	ProjectRoot string
	WriteDirs   []string

	RuleSet  *rulespb.RuleSet
	Checkers *cmdcheck.Registry
	Config   *config.Resolver
}

// Verdict is the AST walker's result.
type Verdict struct {
	// Allowed is true only when every node in the parsed AST cleared the rule set. Parse errors, empty commands, and
	// unmatched commands all collapse to false.
	Allowed bool

	// Matched lists the rule names that fired (source order, duplicates kept). A compound command can legitimately have
	// multiple pieces match — e.g., chained `git` subcommands. Empty when Allowed is false.
	Matched []string
}

// Evaluate parses `in.Command` and walks the resulting AST. All failure modes (malformed JSON, parse error, empty
// command, no matching rule) collapse into a zero [Verdict].
func Evaluate(in Input) Verdict {
	command := strings.TrimSpace(in.Command)
	if command == "" {
		return Verdict{}
	}
	f, err := ParseCommand(command)
	if err != nil || len(f.Stmts) == 0 {
		return Verdict{}
	}
	w := newWalker(in)
	if !w.stmtsAreSafe(f.Stmts) {
		return Verdict{}
	}
	return Verdict{Allowed: true, Matched: w.matched}
}

// ParseCommand parses a bash command string into an AST. Comments are dropped — the walker only inspects executable
// nodes.
func ParseCommand(command string) (*syntax.File, error) {
	return syntax.NewParser(syntax.KeepComments(false)).Parse(
		strings.NewReader(command), "",
	)
}
