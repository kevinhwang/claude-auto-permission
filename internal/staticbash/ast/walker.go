package ast

import (
	"claude-auto-permission/internal/staticbash/cmdcheck"
	"claude-auto-permission/internal/staticbash/policy"
	"claude-auto-permission/internal/staticbash/rules"

	"mvdan.cc/sh/v3/syntax"
)

// shellKeywords are commands that execute arbitrary code and are always rejected.
var shellKeywords = cmdcheck.ToSet("eval", "exec", "source", ".", "trap")

// walker walks a bash AST and determines whether every node is safe. Holds the [Input] it was constructed from plus
// per-walk scratch state (matched rule names, written paths for the write-then-execute check).
type walker struct {
	in Input

	writtenPaths map[string]bool
	matched      []string
}

func newWalker(in Input) *walker {
	return &walker{
		in:           in,
		writtenPaths: make(map[string]bool),
	}
}

// recurse re-enters [Evaluate] for a nested command (subshell, ssh remote-eval, etc). Remote recursion (writeDirs !=
// nil) drops `ProjectRoot` so `allow_project_write` checks don't apply across the host boundary; local recursion
// inherits the parent scope.
func (w *walker) recurse(command, cwd string, writeDirs []string) bool {
	sub := w.in
	sub.Command = command
	sub.Cwd = cwd
	sub.WriteDirs = writeDirs
	if writeDirs != nil {
		sub.ProjectRoot = ""
	}
	return Evaluate(sub).Allowed
}

// remoteHostLookup adapts [policy.MatchRemoteHost] to the [rules.RemoteHostLookup] seam — bound to the walker's
// per-call config so the rules engine doesn't have to know about the config package.
func (w *walker) remoteHostLookup(host, cwd string) (rules.RemoteHostConfig, bool) {
	rh, ok := policy.MatchRemoteHost(w.in.Config, host, cwd)
	if !ok {
		return nil, false
	}
	return rh, true
}

func (w *walker) stmtsAreSafe(stmts []*syntax.Stmt) bool {
	for _, stmt := range stmts {
		if !w.stmtIsSafe(stmt) {
			return false
		}
	}
	return true
}

func (w *walker) stmtIsSafe(stmt *syntax.Stmt) bool {
	if stmt.Background || stmt.Coprocess {
		return false
	}
	for _, redir := range stmt.Redirs {
		if !w.checkRedirect(redir) {
			return false
		}
	}
	if stmt.Cmd == nil {
		return len(stmt.Redirs) == 0
	}
	return w.commandIsSafe(stmt.Cmd)
}

func (w *walker) commandIsSafe(cmd syntax.Command) bool {
	switch c := cmd.(type) {
	case *syntax.CallExpr:
		return w.checkCall(c)
	case *syntax.BinaryCmd:
		return w.stmtIsSafe(c.X) && w.stmtIsSafe(c.Y)
	case *syntax.Subshell:
		return w.stmtsAreSafe(c.Stmts)
	case *syntax.Block:
		return w.stmtsAreSafe(c.Stmts)
	case *syntax.IfClause:
		return w.checkIfClause(c)
	case *syntax.WhileClause:
		return w.stmtsAreSafe(c.Cond) && w.stmtsAreSafe(c.Do)
	case *syntax.ForClause:
		return w.checkForClause(c)
	case *syntax.CaseClause:
		return w.checkCaseClause(c)
	case *syntax.TestClause:
		return w.testExprIsSafe(c.X)
	case *syntax.ArithmCmd:
		return w.arithmExprIsSafe(c.X)
	case *syntax.TimeClause:
		if c.Stmt == nil {
			return true
		}
		return w.stmtIsSafe(c.Stmt)
	case *syntax.FuncDecl:
		return false
	case *syntax.CoprocClause:
		return false
	case *syntax.LetClause:
		for _, expr := range c.Exprs {
			if !w.arithmExprIsSafe(expr) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// checkCall validates common invariants and dispatches to the registered Checker.
func (w *walker) checkCall(call *syntax.CallExpr) bool {
	// Env-var assignments fall through unconditionally. Allowlisting value-only var names would require tracking every
	// interpreter's evolving code-execution env vars (LD_PRELOAD, BASH_ENV, GIT_SSH_COMMAND, NODE_OPTIONS, …); a missed
	// name is a bypass. The bare form (`FOO=bar`) has no symbol table to be read by later commands, so there's no
	// legitimate use case either.
	if len(call.Assigns) > 0 {
		return false
	}
	if len(call.Args) == 0 {
		return false
	}

	name, ok := cmdcheck.CommandName(call.Args[0])
	if !ok {
		return false
	}

	if shellKeywords[name] {
		return false
	}

	// Reject write-then-execute: command resolves to a path written to earlier in this script.
	if w.isWrittenPath(call.Args[0]) {
		return false
	}

	for _, arg := range call.Args[1:] {
		if !w.wordIsSafe(arg) {
			return false
		}
	}

	if spec := rules.LookupCommand(w.in.RuleSet, name); spec != nil {
		ctx := &rules.EvalCtx{
			Cwd:              w.in.Cwd,
			WriteDirs:        w.in.WriteDirs,
			IsPathAllowed:    w.isPathAllowed,
			Evaluate:         w.recurse,
			RemoteHostLookup: w.remoteHostLookup,
			RuleSet:          w.in.RuleSet,
			Checkers:         w.in.Checkers,
		}
		if rules.Evaluate(spec, ctx, call.Args) {
			w.matched = append(w.matched, name)
			return true
		}
		return false
	}

	// Fall back to the checker Registry (sed/awk builtins) for commands without a rule-engine spec.
	if w.in.Checkers != nil {
		if c, ok := w.in.Checkers.Lookup(name); ok {
			ctx := &cmdcheck.Context{
				Cwd:           w.in.Cwd,
				WriteDirs:     w.in.WriteDirs,
				IsPathAllowed: w.isPathAllowed,
				Evaluate:      w.recurse,
			}
			if c.Check(ctx, call.Args) {
				w.matched = append(w.matched, name)
				return true
			}
			return false
		}
	}
	return false
}

func (w *walker) checkIfClause(ic *syntax.IfClause) bool {
	if !w.stmtsAreSafe(ic.Cond) || !w.stmtsAreSafe(ic.Then) {
		return false
	}
	if ic.Else != nil {
		if !w.checkIfClause(ic.Else) {
			return false
		}
	}
	return true
}

func (w *walker) checkForClause(fc *syntax.ForClause) bool {
	switch loop := fc.Loop.(type) {
	case *syntax.WordIter:
		for _, item := range loop.Items {
			if !w.wordIsSafe(item) {
				return false
			}
		}
	case *syntax.CStyleLoop:
		if !w.arithmExprIsSafe(loop.Init) || !w.arithmExprIsSafe(loop.Cond) || !w.arithmExprIsSafe(loop.Post) {
			return false
		}
	default:
		return false
	}
	return w.stmtsAreSafe(fc.Do)
}

func (w *walker) checkCaseClause(cc *syntax.CaseClause) bool {
	if !w.wordIsSafe(cc.Word) {
		return false
	}
	for _, item := range cc.Items {
		for _, pattern := range item.Patterns {
			if !w.wordIsSafe(pattern) {
				return false
			}
		}
		if !w.stmtsAreSafe(item.Stmts) {
			return false
		}
	}
	return true
}
