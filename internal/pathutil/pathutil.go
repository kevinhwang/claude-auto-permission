// Package pathutil holds filesystem-path helpers: tilde expansion, symlink-aware resolution, "is X under Y" checks,
// doublestar glob matching. No dependencies on other internal packages.
package pathutil

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// RealPath resolves symlinks and cleans the path. For paths that don't fully exist (e.g., a new file being created), it
// resolves the longest existing prefix and appends the rest — critical on macOS where /tmp is a symlink to
// /private/tmp.
func RealPath(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err == nil {
		return resolved
	}
	cleaned := filepath.Clean(path)
	rest := ""
	cur := cleaned
	for {
		resolved, err := filepath.EvalSymlinks(cur)
		if err == nil {
			if rest == "" {
				return resolved
			}
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		if rest == "" {
			rest = filepath.Base(cur)
		} else {
			rest = filepath.Join(filepath.Base(cur), rest)
		}
		cur = parent
	}
	return cleaned
}

// ExpandTilde expands a leading ~ to the user's home directory. Returns path unchanged if it doesn't start with ~ or if
// HOME is unresolvable.
func ExpandTilde(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		if path == "~" {
			return home
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

// IsPathUnder checks if path resolves to within dir.
func IsPathUnder(path, dir string) bool {
	resolved := RealPath(path)
	dirResolved := RealPath(dir)
	return resolved == dirResolved || strings.HasPrefix(resolved, dirResolved+string(os.PathSeparator))
}

// GlobMatch reports whether path matches a doublestar pattern. Matcher errors are treated as "no match" so a malformed
// pattern can't crash the hook.
func GlobMatch(pattern, path string) bool {
	matched, _ := doublestar.Match(pattern, path)
	return matched
}

// MatchResolved tries glob matching against both the raw and symlink-resolved path. Handles cases like /tmp →
// /private/tmp on macOS.
func MatchResolved(pattern, path string) bool {
	if GlobMatch(pattern, path) {
		return true
	}
	resolved := RealPath(path)
	if resolved != path {
		return GlobMatch(pattern, resolved)
	}
	return false
}
