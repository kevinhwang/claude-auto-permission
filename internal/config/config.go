// Package config wraps the proto Config in a [Resolver] that answers per-cwd project lookups. Domain-specific queries
// built on top of those lookups (write-allow checks, remote-host matching) live in their consumer packages — see
// `internal/staticbash/policy` for the structural-judge questions.
package config

import (
	configpb "claude-auto-permission/internal/gen/config/v1"
	"claude-auto-permission/internal/pathutil"
)

// Resolver wraps a proto Config. Safe for concurrent reads; the proto isn't mutated after construction.
type Resolver struct {
	cfg *configpb.Config
}

// NewResolver wraps cfg. Nil cfg yields the empty (most-restrictive) config so callers don't have to nil-check.
func NewResolver(cfg *configpb.Config) *Resolver {
	if cfg == nil {
		cfg = &configpb.Config{}
	}
	return &Resolver{cfg: cfg}
}

// Proto returns the underlying proto for direct read access. Callers must not mutate it.
func (r *Resolver) Proto() *configpb.Config { return r.cfg }

// MatchingProjects returns all projects whose `path_patterns` cover projectRoot, in declaration order. projectRoot
// should be `$CLAUDE_PROJECT_DIR` when available, falling back to cwd.
func (r *Resolver) MatchingProjects(projectRoot string) []*configpb.Project {
	var result []*configpb.Project
	for _, proj := range r.cfg.GetProjects() {
		if projectMatches(proj, projectRoot) {
			result = append(result, proj)
		}
	}
	return result
}

// MatchingLlmClassifier returns the [configpb.LlmClassifierConfig] from the most-specific matching project (longest
// matching `path_pattern`), or nil when no project covers projectRoot.
func (r *Resolver) MatchingLlmClassifier(projectRoot string) *configpb.LlmClassifierConfig {
	var best *configpb.LlmClassifierConfig
	bestSpecificity := -1
	for _, proj := range r.MatchingProjects(projectRoot) {
		if proj.GetLlmClassifier() == nil {
			continue
		}
		sp := projectSpecificity(proj, projectRoot)
		if sp >= bestSpecificity {
			best = proj.GetLlmClassifier()
			bestSpecificity = sp
		}
	}
	return best
}

func projectMatches(proj *configpb.Project, projectRoot string) bool {
	for _, pattern := range proj.GetPathPatterns() {
		expanded := pathutil.ExpandTilde(pattern)
		if pathutil.MatchResolved(expanded, projectRoot) {
			return true
		}
	}
	return false
}

func projectSpecificity(proj *configpb.Project, projectRoot string) int {
	best := 0
	for _, pattern := range proj.GetPathPatterns() {
		expanded := pathutil.ExpandTilde(pattern)
		if pathutil.MatchResolved(expanded, projectRoot) && len(expanded) > best {
			best = len(expanded)
		}
	}
	return best
}
