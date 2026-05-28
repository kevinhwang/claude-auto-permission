package ast

import (
	"strings"
	"testing"

	"mvdan.cc/sh/v3/syntax"
)

// testInput builds an Input wired up with the package-level test fixtures (rule set, checker registry, config
// resolver). cwd defaults to "/project" when empty so unit tests don't have to repeat themselves.
func testInput(cwd string) Input {
	if cwd == "" {
		cwd = "/project"
	}
	return Input{
		Cwd:         cwd,
		ProjectRoot: cwd,
		RuleSet:     testRuleSet,
		Checkers:    testCheckers,
		Config:      testCfg,
	}
}

func parseStmt(t *testing.T, cmd string) *syntax.Stmt {
	t.Helper()
	f, err := syntax.NewParser().Parse(strings.NewReader(cmd), "")
	if err != nil {
		t.Fatalf("parse %q: %v", cmd, err)
	}
	if len(f.Stmts) != 1 {
		t.Fatalf("expected 1 stmt, got %d for %q", len(f.Stmts), cmd)
	}
	return f.Stmts[0]
}

func TestStmtIsSafe_Background(t *testing.T) {
	w := newWalker(testInput(""))
	stmt := parseStmt(t, "echo hello &")
	if w.stmtIsSafe(stmt) {
		t.Error("background command should not be safe")
	}
}

func TestStmtIsSafe_Negation(t *testing.T) {
	w := newWalker(testInput(""))
	stmt := parseStmt(t, "! git diff --quiet")
	if !w.stmtIsSafe(stmt) {
		t.Error("negated safe command should be safe")
	}
}

func TestCommandIsSafe_IfClause(t *testing.T) {
	w := newWalker(testInput(""))
	tests := []struct {
		name string
		cmd  string
		safe bool
	}{
		{name: "safe if", cmd: "if git diff --quiet; then echo clean; fi", safe: true},
		{name: "safe if-else", cmd: "if git diff --quiet; then echo clean; else echo dirty; fi", safe: true},
		{name: "unsafe cond", cmd: "if rm -rf /; then echo done; fi", safe: false},
		{name: "unsafe then", cmd: "if true; then rm -rf /; fi", safe: false},
		{name: "unsafe else", cmd: "if true; then echo ok; else rm -rf /; fi", safe: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stmt := parseStmt(t, tt.cmd)
			if got := w.commandIsSafe(stmt.Cmd); got != tt.safe {
				t.Errorf("commandIsSafe(%q) = %v, want %v", tt.cmd, got, tt.safe)
			}
		})
	}
}

func TestCommandIsSafe_ForClause(t *testing.T) {
	w := newWalker(testInput(""))
	tests := []struct {
		name string
		cmd  string
		safe bool
	}{
		{name: "safe for", cmd: `for f in *.txt; do cat "$f"; done`, safe: true},
		{name: "safe c-style for", cmd: `for ((i=0; i<10; i++)); do echo $i; done`, safe: true},
		{name: "unsafe body", cmd: `for f in *; do rm "$f"; done`, safe: false},
		{name: "unsafe items", cmd: `for f in $(rm -rf /); do echo "$f"; done`, safe: false},
		{name: "unsafe c-style init", cmd: `for ((i=$(rm -rf /); i<3; i++)); do echo $i; done`, safe: false},
		{name: "unsafe c-style cond", cmd: `for ((i=0; $(rm -rf /); i++)); do echo $i; done`, safe: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stmt := parseStmt(t, tt.cmd)
			if got := w.commandIsSafe(stmt.Cmd); got != tt.safe {
				t.Errorf("commandIsSafe(%q) = %v, want %v", tt.cmd, got, tt.safe)
			}
		})
	}
}

func TestCommandIsSafe_Subshell(t *testing.T) {
	w := newWalker(testInput(""))
	tests := []struct {
		name string
		cmd  string
		safe bool
	}{
		{name: "safe subshell", cmd: "(cd dir && git status)", safe: true},
		{name: "unsafe subshell", cmd: "(rm -rf /)", safe: false},
		{name: "nested safe", cmd: "(cd dir && (echo a; echo b))", safe: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stmt := parseStmt(t, tt.cmd)
			if got := w.commandIsSafe(stmt.Cmd); got != tt.safe {
				t.Errorf("commandIsSafe(%q) = %v, want %v", tt.cmd, got, tt.safe)
			}
		})
	}
}

func TestCommandIsSafe_FuncDecl(t *testing.T) {
	w := newWalker(testInput(""))
	stmt := parseStmt(t, "foo() { echo hello; }")
	if w.commandIsSafe(stmt.Cmd) {
		t.Error("function declaration should not be safe")
	}
}

func TestCommandIsSafe_TestClause(t *testing.T) {
	w := newWalker(testInput(""))
	tests := []struct {
		name string
		cmd  string
		safe bool
	}{
		{name: "file test", cmd: "[[ -f file.txt ]]", safe: true},
		{name: "string compare", cmd: `[[ "$FOO" == "bar" ]]`, safe: true},
		{name: "safe subst in test", cmd: `[[ "$(echo ok)" == "ok" ]]`, safe: true},
		{name: "unsafe subst in test", cmd: `[[ "$(rm -rf /)" == "" ]]`, safe: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stmt := parseStmt(t, tt.cmd)
			if got := w.commandIsSafe(stmt.Cmd); got != tt.safe {
				t.Errorf("commandIsSafe(%q) = %v, want %v", tt.cmd, got, tt.safe)
			}
		})
	}
}

func TestCommandIsSafe_WhileClause(t *testing.T) {
	w := newWalker(testInput(""))
	tests := []struct {
		name string
		cmd  string
		safe bool
	}{
		{name: "safe while", cmd: "while ! grep -q ready status.txt; do sleep 1; done", safe: true},
		{name: "unsafe body", cmd: "while true; do rm -rf /; done", safe: false},
		{name: "unsafe cond", cmd: "while rm -rf /; do echo done; done", safe: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stmt := parseStmt(t, tt.cmd)
			if got := w.commandIsSafe(stmt.Cmd); got != tt.safe {
				t.Errorf("commandIsSafe(%q) = %v, want %v", tt.cmd, got, tt.safe)
			}
		})
	}
}

func TestCommandIsSafe_CaseClause(t *testing.T) {
	w := newWalker(testInput(""))
	tests := []struct {
		name string
		cmd  string
		safe bool
	}{
		{name: "safe case", cmd: `case "$1" in *.txt) echo txt;; *.md) echo md;; esac`, safe: true},
		{name: "unsafe body", cmd: `case x in *) rm -rf /;; esac`, safe: false},
		{name: "unsafe word subst", cmd: `case "$(rm -rf /)" in *) echo match;; esac`, safe: false},
		{name: "unsafe pattern subst", cmd: `case x in $(rm -rf /)) echo match;; esac`, safe: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stmt := parseStmt(t, tt.cmd)
			if got := w.commandIsSafe(stmt.Cmd); got != tt.safe {
				t.Errorf("commandIsSafe(%q) = %v, want %v", tt.cmd, got, tt.safe)
			}
		})
	}
}

func TestCommandIsSafe_Block(t *testing.T) {
	w := newWalker(testInput(""))
	tests := []struct {
		name string
		cmd  string
		safe bool
	}{
		{name: "safe block", cmd: "{ echo a; echo b; }", safe: true},
		{name: "unsafe block", cmd: "{ rm -rf /; }", safe: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stmt := parseStmt(t, tt.cmd)
			if got := w.commandIsSafe(stmt.Cmd); got != tt.safe {
				t.Errorf("commandIsSafe(%q) = %v, want %v", tt.cmd, got, tt.safe)
			}
		})
	}
}

func TestCommandIsSafe_ArithmAndLet(t *testing.T) {
	w := newWalker(testInput(""))
	tests := []struct {
		name string
		cmd  string
		safe bool
	}{
		{name: "safe arithm", cmd: "(( x = 1 + 2 ))", safe: true},
		{name: "unsafe arithm subst", cmd: `(( x = $(rm -rf /) ))`, safe: false},
		{name: "safe let", cmd: "let x=1+2", safe: true},
		{name: "unsafe let subst", cmd: `let "x=$(rm -rf /)+1"`, safe: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stmt := parseStmt(t, tt.cmd)
			if got := w.commandIsSafe(stmt.Cmd); got != tt.safe {
				t.Errorf("commandIsSafe(%q) = %v, want %v", tt.cmd, got, tt.safe)
			}
		})
	}
}

func TestCommandIsSafe_TimeClause(t *testing.T) {
	w := newWalker(testInput(""))
	tests := []struct {
		name string
		cmd  string
		safe bool
	}{
		{name: "safe time", cmd: "time git status", safe: true},
		{name: "bare time", cmd: "time", safe: true},
		{name: "unsafe time", cmd: "time rm -rf /", safe: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stmt := parseStmt(t, tt.cmd)
			if got := w.commandIsSafe(stmt.Cmd); got != tt.safe {
				t.Errorf("commandIsSafe(%q) = %v, want %v", tt.cmd, got, tt.safe)
			}
		})
	}
}

func TestCommandIsSafe_Coproc(t *testing.T) {
	w := newWalker(testInput(""))
	stmt := parseStmt(t, "coproc cat")
	if w.commandIsSafe(stmt.Cmd) {
		t.Error("coproc should not be safe")
	}
}
