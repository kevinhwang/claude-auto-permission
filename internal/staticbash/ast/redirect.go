package ast

import (
	"path/filepath"
	"strings"

	"claude-auto-permission/internal/pathutil"
	"claude-auto-permission/internal/staticbash/cmdcheck"
	"claude-auto-permission/internal/staticbash/policy"

	"mvdan.cc/sh/v3/syntax"
)

var safeRedirectTargets = cmdcheck.ToSet("/dev/null")

func (w *walker) checkRedirect(redir *syntax.Redirect) bool {
	switch redir.Op {
	case syntax.RdrIn:
		return true
	case syntax.DplOut, syntax.DplIn:
		return true
	case syntax.Hdoc, syntax.DashHdoc, syntax.WordHdoc:
		if redir.Hdoc != nil {
			return w.wordIsSafe(redir.Hdoc)
		}
		return true
	case syntax.RdrOut, syntax.AppOut, syntax.ClbOut, syntax.RdrAll, syntax.AppAll:
		return w.isOutputRedirectSafe(redir)
	case syntax.RdrInOut:
		return false
	default:
		return false
	}
}

func (w *walker) isOutputRedirectSafe(redir *syntax.Redirect) bool {
	target, ok := cmdcheck.LiteralString(redir.Word)
	if !ok {
		return false
	}
	if !w.isPathAllowed(target) {
		return false
	}
	w.recordWrittenPath(target)
	return true
}

// recordWrittenPath adds a resolved path to the written set.
func (w *walker) recordWrittenPath(path string) {
	resolved := w.resolveToAbsolute(path)
	if resolved != "" {
		w.writtenPaths[resolved] = true
	}
}

// isWrittenPath checks whether a command word resolves to a path that was previously written to in this script.
func (w *walker) isWrittenPath(word *syntax.Word) bool {
	cmdStr, ok := cmdcheck.LiteralString(word)
	if !ok {
		return false
	}
	resolved := w.resolveToAbsolute(cmdStr)
	return resolved != "" && w.writtenPaths[resolved]
}

// resolveToAbsolute resolves a path to an absolute, cleaned path.
func (w *walker) resolveToAbsolute(path string) string {
	path = pathutil.ExpandTilde(path)
	if !filepath.IsAbs(path) {
		if w.in.Cwd == "" {
			return ""
		}
		path = filepath.Join(w.in.Cwd, path)
	}
	return pathutil.RealPath(path)
}

// isPathAllowed checks if a path is writable. For local commands, it uses the project-scoped config. For remote
// commands (writeDirs is non-nil), it glob-matches against the remote host's allow_write_patterns.
func (w *walker) isPathAllowed(path string) bool {
	if safeRedirectTargets[path] {
		return true
	}
	if w.in.Cwd == "" {
		return false
	}

	// Remote commands: glob-match against provided write patterns. Don't expand ~ locally — remote paths are matched
	// literally.
	if len(w.in.WriteDirs) > 0 {
		return w.isRemotePathAllowed(path)
	}

	// Local commands: expand ~, resolve relative paths, then ask the staticbash policy layer (which itself handles symlink
	// resolution).
	path = pathutil.ExpandTilde(path)
	if !filepath.IsAbs(path) {
		path = filepath.Join(w.in.Cwd, path)
	}
	return policy.IsWriteAllowed(w.in.Config, path, w.in.Cwd, w.in.ProjectRoot)
}

// isRemotePathAllowed glob-matches a remote path against the remote host's allow_write_patterns. Relative paths (not
// starting with / or ~/) are resolved against the remote cwd if available.
func (w *walker) isRemotePathAllowed(path string) bool {
	if !filepath.IsAbs(path) && !strings.HasPrefix(path, "~/") {
		if w.in.Cwd != "" {
			path = filepath.Join(w.in.Cwd, path)
		} else {
			return false
		}
	}
	for _, pattern := range w.in.WriteDirs {
		if pathutil.GlobMatch(pattern, path) {
			return true
		}
	}
	return false
}
