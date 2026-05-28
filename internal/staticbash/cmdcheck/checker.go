// Package cmdcheck is the shared low-level vocabulary of the `staticbash` subsystem:
//
//   - The [Checker] interface and [Registry], used by the AST walker to dispatch per-command checks (sed, awk).
//   - Shell-word helpers ([LiteralString], [CommandName], [HasWriteFlags], [ToSet]) used by `ast`, `rules`, and
//     `builtins` alike.
//
// Lives at the bottom of the subsystem's import graph so any other `staticbash` sub-package can pull it in without
// cycle pressure.
package cmdcheck

import "mvdan.cc/sh/v3/syntax"

// Context is the evaluation environment the AST walker hands to a [Checker]. The function fields let checkers reuse
// walker capabilities (path-allow checks, recursive command evaluation) without importing the walker package and
// creating a cycle.
type Context struct {
	Cwd       string
	WriteDirs []string

	IsPathAllowed func(path string) bool
	Evaluate      func(command, cwd string, writeDirs []string) bool
}

// Checker evaluates whether a command invocation is safe. args[0] is the command name.
type Checker interface {
	Check(ctx *Context, args []*syntax.Word) bool
}
