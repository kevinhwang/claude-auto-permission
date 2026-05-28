package ast

import "testing"

func TestIsPathAllowed(t *testing.T) {
	w := newWalker(testInput(""))
	tests := []struct {
		name  string
		path  string
		allow bool
	}{
		{name: "/dev/null", path: "/dev/null", allow: true},
		{name: "relative in project", path: "output.txt", allow: true},
		{name: "absolute in project", path: "/project/output.txt", allow: true},
		{name: "nested in project", path: "/project/deep/nested/file.txt", allow: true},
		{name: "/tmp file", path: "/tmp/output.txt", allow: true},
		{name: "/tmp nested", path: "/tmp/deep/file.txt", allow: true},
		{name: "/etc", path: "/etc/passwd", allow: false},
		{name: "traversal", path: "../../etc/passwd", allow: false},
		{name: "absolute outside", path: "/usr/local/bin/evil", allow: false},
		{name: "tilde home", path: "~/.bashrc", allow: false},
		{name: "tilde subdir", path: "~/evil/file", allow: false},
		{name: "tilde traversal", path: "~/../../etc/passwd", allow: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := w.isPathAllowed(tt.path); got != tt.allow {
				t.Errorf("isPathAllowed(%q) = %v, want %v", tt.path, got, tt.allow)
			}
		})
	}
}

func TestIsPathAllowed_NoCwd(t *testing.T) {
	w := newWalker(Input{
		RuleSet:  testRuleSet,
		Checkers: testCheckers,
		Config:   testCfg,
	})
	tests := []struct {
		name  string
		path  string
		allow bool
	}{
		{name: "/dev/null still works", path: "/dev/null", allow: true},
		{name: "relative file denied", path: "output.txt", allow: false},
		{name: "/tmp denied without cwd", path: "/tmp/file.txt", allow: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := w.isPathAllowed(tt.path); got != tt.allow {
				t.Errorf("isPathAllowed(%q) = %v, want %v", tt.path, got, tt.allow)
			}
		})
	}
}
