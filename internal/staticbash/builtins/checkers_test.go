package builtins

import (
	"strings"
	"testing"

	"claude-auto-permission/internal/staticbash/ast"
	"claude-auto-permission/internal/staticbash/cmdcheck"

	"mvdan.cc/sh/v3/syntax"
)

func parseCall(t *testing.T, cmd string) []*syntax.Word {
	t.Helper()
	f, err := syntax.NewParser().Parse(strings.NewReader(cmd), "")
	if err != nil {
		t.Fatalf("parse %q: %v", cmd, err)
	}
	if len(f.Stmts) != 1 {
		t.Fatalf("expected 1 stmt, got %d", len(f.Stmts))
	}
	call, ok := f.Stmts[0].Cmd.(*syntax.CallExpr)
	if !ok {
		t.Fatalf("expected CallExpr, got %T", f.Stmts[0].Cmd)
	}
	return call.Args
}

func testContext(cwd string) *cmdcheck.Context {
	return &cmdcheck.Context{
		Cwd:       cwd,
		WriteDirs: []string{"/tmp"},
		IsPathAllowed: func(path string) bool {
			return false
		},
		Evaluate: func(command, cwd string, writeDirs []string) bool {
			return ast.Evaluate(ast.Input{
				Command:   command,
				Cwd:       cwd,
				WriteDirs: writeDirs,
			}).Allowed
		},
	}
}

func TestSedChecker(t *testing.T) {
	ctx := testContext("/project")
	tests := []struct {
		name  string
		cmd   string
		allow bool
	}{
		{name: "simple subst", cmd: "sed 's/foo/bar/g'", allow: true},
		{name: "delete pattern", cmd: "sed '/pattern/d'", allow: true},
		{name: "multiple -e", cmd: "sed -e 's/a/b/' -e '/x/d'", allow: true},
		{name: "translate", cmd: "sed 'y/abc/xyz/'", allow: true},
		{name: "print", cmd: "sed -n '/foo/p'", allow: true},
		{name: "address range", cmd: "sed '1,10d'", allow: true},
		{name: "dollar address", cmd: "sed '$d'", allow: true},
		{name: "hold space", cmd: "sed -n 'h;n;H;g;p'", allow: true},
		{name: "branch", cmd: "sed ':a;N;$!ba;s/\\n/ /g'", allow: true},
		{name: "append text", cmd: `sed '/foo/a\new line'`, allow: true},
		{name: "read file", cmd: "sed '/foo/r input.txt'", allow: true},
		// Dangerous commands
		{name: "e flag on subst", cmd: "sed 's/foo/bar/e'", allow: false},
		{name: "w command", cmd: "sed 'w /etc/evil'", allow: false},
		{name: "W command", cmd: "sed 'W /etc/evil'", allow: false},
		{name: "e command", cmd: "sed 'e'", allow: false},
		{name: "w flag on subst", cmd: "sed 's/foo/bar/w /tmp/out'", allow: false},
		{name: "-i flag", cmd: "sed -i 's/foo/bar/' file", allow: false},
		{name: "--in-place", cmd: "sed --in-place 's/foo/bar/' file", allow: false},
		{name: "-f script", cmd: "sed -f script.sed file", allow: false},
	}
	c := sedChecker{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := parseCall(t, tt.cmd)
			if got := c.Check(ctx, args); got != tt.allow {
				t.Errorf("sedChecker.Check(%q) = %v, want %v", tt.cmd, got, tt.allow)
			}
		})
	}
}

func TestAwkChecker(t *testing.T) {
	ctx := testContext("/project")
	tests := []struct {
		name  string
		cmd   string
		allow bool
	}{
		{name: "simple print", cmd: "awk '{print $1}'", allow: true},
		{name: "field separator", cmd: "awk -F: '{print $1}'", allow: true},
		{name: "comparison >=", cmd: "awk '{if (a >= b) print}'", allow: true},
		{name: "comparison >", cmd: "awk '$3 > 100' file.txt", allow: true},
		{name: "NR comparison", cmd: "awk 'NR > 5 && NR < 10' file.txt", allow: true},
		{name: "regex alternation", cmd: "awk '/foo|bar/ {print}' file.txt", allow: true},
		{name: "logical or ||", cmd: "awk '{if (a || b) print}'", allow: true},
		{name: "with -v", cmd: "awk -v x=1 '{print x}'", allow: true},
		{name: "with -e", cmd: "awk -e '{print $1}'", allow: true},
		// Dangerous patterns
		{name: "redirect to var", cmd: "awk -v f=/etc/passwd '{print > f}'", allow: false},
		{name: "redirect to literal", cmd: `awk '{print > "/tmp/out"}'`, allow: false},
		{name: "redirect no space", cmd: `awk '{print >"/tmp/out"}'`, allow: false},
		{name: "redirect abs path", cmd: `awk '{print > /tmp/file}' input`, allow: false},
		{name: "append >>", cmd: `awk '{print >> "file"}'`, allow: false},
		{name: "pipe out", cmd: `awk '{print | "cmd"}'`, allow: false},
		{name: "system call", cmd: `awk '{system("ls")}'`, allow: false},
		{name: "getline", cmd: "awk '{getline line}'", allow: false},
		{name: "-f script", cmd: "awk -f script.awk", allow: false},
	}
	c := awkChecker{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := parseCall(t, tt.cmd)
			if got := c.Check(ctx, args); got != tt.allow {
				t.Errorf("awkChecker.Check(%q) = %v, want %v", tt.cmd, got, tt.allow)
			}
		})
	}
}
