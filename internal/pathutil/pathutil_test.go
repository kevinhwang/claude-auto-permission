package pathutil

import "testing"

func TestGlobMatch(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		path    string
		match   bool
	}{
		{name: "doublestar", pattern: "/tmp/**", path: "/tmp/deep/file", match: true},
		{name: "exact", pattern: "/tmp/file", path: "/tmp/file", match: true},
		{name: "no match", pattern: "/tmp/**", path: "/etc/file", match: false},
		{name: "single star", pattern: "/tmp/*", path: "/tmp/file", match: true},
		{name: "single star no depth", pattern: "/tmp/*", path: "/tmp/a/b", match: false},
		{name: "question mark", pattern: "/tmp/?.txt", path: "/tmp/a.txt", match: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GlobMatch(tt.pattern, tt.path); got != tt.match {
				t.Errorf("GlobMatch(%q, %q) = %v, want %v", tt.pattern, tt.path, got, tt.match)
			}
		})
	}
}
