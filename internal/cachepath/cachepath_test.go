package cachepath

import (
	"path/filepath"
	"testing"
)

func TestExpand_PassesThroughAbsolute(t *testing.T) {
	dir := t.TempDir()
	if got := Expand(dir); got != dir {
		t.Errorf("Expand(%q) = %q, want passthrough", dir, got)
	}
}

func TestExpand_ExpandsTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got := Expand("~/sub")
	want := filepath.Join(home, "sub")
	if got != want {
		t.Errorf("Expand(~/sub) = %q, want %q", got, want)
	}
}

func TestExpand_EmptyInputEmptyOutput(t *testing.T) {
	if got := Expand(""); got != "" {
		t.Errorf("Expand(\"\") = %q, want empty", got)
	}
}

func TestSubpathHelpers(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name string
		fn   func(string) string
		want string
	}{
		{"decisions.log.jsonl", DecisionsLog, "decisions.log.jsonl"},
		{"sessions/", SessionsDir, "sessions"},
		{"auto_mode_policy_cache/", AutoModePolicyCacheDir, "auto_mode_policy_cache"},
		{"dumps/", DumpsDir, "dumps"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.fn(root)
			want := filepath.Join(root, tt.want)
			if got != want {
				t.Errorf("got %q, want %q", got, want)
			}
		})
	}
}

// Empty root must propagate as empty so callers can opt out of on-disk state without each one having to special-case
// the empty path. Without this, joinUnderRoot would produce relative paths like "auto_mode_policy_cache" that get
// created in cwd.
func TestSubpathHelpers_EmptyRootEmptyOutput(t *testing.T) {
	for _, fn := range []func(string) string{DecisionsLog, SessionsDir, AutoModePolicyCacheDir, DumpsDir} {
		if got := fn(""); got != "" {
			t.Errorf("fn(\"\") = %q, want empty", got)
		}
	}
}
