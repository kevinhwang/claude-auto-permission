// Package cachepath derives subpaths under the claude-auto-permission cache root: the per-session backstop counters,
// the decisions JSONL log, the auto-mode policy cache, and (when `dump_classifier` is on) classifier req/res dumps.
//
// Centralizing the joins here keeps a typo from silently scattering state across the disk. Every helper short-circuits
// on an empty root so callers can disable on-disk state by passing `""`.
package cachepath

import (
	"path/filepath"

	"claude-auto-permission/internal/pathutil"
)

// On-disk subpath names — joined onto a cache root via the helpers below.
const (
	decisionsLogName      = "decisions.log.jsonl"
	sessionsDirName       = "sessions"
	autoModePolicyDirName = "auto_mode_policy_cache"
	dumpsDirName          = "dumps"
)

// Expand returns root with a leading `~` expanded against `$HOME`. Empty input returns empty so callers can opt out of
// on-disk state.
func Expand(root string) string {
	if root == "" {
		return ""
	}
	return pathutil.ExpandTilde(root)
}

// DecisionsLog returns the path to the JSONL decisions log under root, or "" when root is empty.
func DecisionsLog(root string) string {
	return joinUnderExpanded(root, decisionsLogName)
}

// SessionsDir returns the directory holding per-session backstop counters under root, or "" when root is empty.
func SessionsDir(root string) string {
	return joinUnderExpanded(root, sessionsDirName)
}

// AutoModePolicyCacheDir returns the directory holding cached `claude auto-mode config` output — one file per (binary,
// settings) fingerprint so concurrent hooks with distinct keys never contend. Returns "" when root is empty.
func AutoModePolicyCacheDir(root string) string {
	return joinUnderExpanded(root, autoModePolicyDirName)
}

// DumpsDir returns the directory holding classifier req/res dumps under root, or "" when root is empty.
func DumpsDir(root string) string {
	return joinUnderExpanded(root, dumpsDirName)
}

// joinUnderExpanded joins name onto root, returning "" when root is empty so callers can opt out of on-disk state
// cleanly without having to special-case the empty path themselves.
func joinUnderExpanded(root, name string) string {
	r := Expand(root)
	if r == "" {
		return ""
	}
	return filepath.Join(r, name)
}
