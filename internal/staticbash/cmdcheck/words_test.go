package cmdcheck

import (
	"strings"
	"testing"

	"mvdan.cc/sh/v3/syntax"
)

func parseWord(t *testing.T, s string) *syntax.Word {
	t.Helper()
	f, err := syntax.NewParser().Parse(strings.NewReader("echo "+s), "")
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	call := f.Stmts[0].Cmd.(*syntax.CallExpr)
	if len(call.Args) < 2 {
		t.Fatalf("expected at least 2 args, got %d", len(call.Args))
	}
	return call.Args[1]
}

func TestLiteralString(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		want   string
		wantOK bool
	}{
		{name: "plain", input: "hello", want: "hello", wantOK: true},
		{name: "single quoted", input: "'hello world'", want: "hello world", wantOK: true},
		{name: "double quoted lit", input: `"hello"`, want: "hello", wantOK: true},
		{name: "path", input: "/usr/bin/git", want: "/usr/bin/git", wantOK: true},
		{name: "param exp", input: "$HOME", want: "", wantOK: false},
		{name: "cmd subst", input: "$(echo hi)", want: "", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			word := parseWord(t, tt.input)
			got, ok := LiteralString(word)
			if ok != tt.wantOK {
				t.Errorf("LiteralString(%q): ok = %v, want %v", tt.input, ok, tt.wantOK)
			}
			if got != tt.want {
				t.Errorf("LiteralString(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestCommandName(t *testing.T) {
	tests := []struct {
		name   string
		cmd    string
		want   string
		wantOK bool
	}{
		{name: "simple", cmd: "git", want: "git", wantOK: true},
		{name: "path", cmd: "/usr/bin/git", want: "git", wantOK: true},
		{name: "quoted", cmd: "'git'", want: "git", wantOK: true},
		{name: "expansion", cmd: "$CMD", want: "", wantOK: false},
		{name: "subst", cmd: "$(echo git)", want: "", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, _ := syntax.NewParser().Parse(strings.NewReader(tt.cmd+" arg"), "")
			call := f.Stmts[0].Cmd.(*syntax.CallExpr)
			got, ok := CommandName(call.Args[0])
			if ok != tt.wantOK {
				t.Errorf("CommandName(%q): ok = %v, want %v", tt.cmd, ok, tt.wantOK)
			}
			if got != tt.want {
				t.Errorf("CommandName(%q) = %q, want %q", tt.cmd, got, tt.want)
			}
		})
	}
}
