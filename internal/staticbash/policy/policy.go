// Package policy answers per-cwd questions the static-bash subsystem asks about the user's project config:
//
//   - "Is this path writable?" — derived from each matching project's `allow_write_patterns` and `allow_project_write`.
//   - "What's the remote-host scope for ssh/tsh recursion?" — derived from `remote_hosts.host_patterns`.
//
// Both queries walk only the [config.Resolver] surface; this package is the natural home because the questions are
// domain-specific to the structural judge.
package policy

import (
	"claude-auto-permission/internal/config"
	configpb "claude-auto-permission/internal/gen/config/v1"
	"claude-auto-permission/internal/pathutil"
)

// IsWriteAllowed reports whether `path` is writable according to the matching projects' `allow_write_patterns` and
// `allow_project_write`. `projectRoot` gates `allow_project_write` (the path must be under projectRoot, not merely
// match any path_patterns). nil cfg returns false.
func IsWriteAllowed(cfg *config.Resolver, path, cwd, projectRoot string) bool {
	if cfg == nil {
		return false
	}
	for _, proj := range cfg.MatchingProjects(cwd) {
		sbr := proj.GetStaticBashRules()
		if sbr.GetAllowProjectWrite() && projectRoot != "" {
			if pathutil.IsPathUnder(path, projectRoot) {
				return true
			}
		}
		for _, pattern := range sbr.GetAllowWritePatterns() {
			pattern = pathutil.ExpandTilde(pattern)
			if pathutil.MatchResolved(pattern, path) {
				return true
			}
		}
	}
	return false
}

// MatchRemoteHost finds the first remote-host config across the matching projects whose `host_patterns` covers `host`.
// nil cfg or no match returns `(nil, false)`.
func MatchRemoteHost(cfg *config.Resolver, host, cwd string) (*configpb.RemoteHost, bool) {
	if cfg == nil {
		return nil, false
	}
	for _, proj := range cfg.MatchingProjects(cwd) {
		for _, rh := range proj.GetStaticBashRules().GetRemoteHosts() {
			for _, pattern := range rh.GetHostPatterns() {
				if pathutil.GlobMatch(pattern, host) {
					return rh, true
				}
			}
		}
	}
	return nil, false
}
