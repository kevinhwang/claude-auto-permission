// Package paths resolves the user-tier filesystem locations Claude Code reads from — settings.json files and CLAUDE.md
// — so consumers don't re-derive HOME / `CLAUDE_CONFIG_DIR` / `CLAUDE_PROJECT_DIR` independently.
package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"claude-auto-permission/internal/pathutil"
)

// Paths holds the resolved Claude Code user-tier locations (settings.json, CLAUDE.md, …).
type Paths struct {
	// ConfigDir is ~/.claude by default, overridden by CLAUDE_CONFIG_DIR. Tilde-expanded and NFC-normalized to match
	// Claude Code.
	ConfigDir string

	// ProjectRoot is stable across worktree moves. Read from $CLAUDE_PROJECT_DIR; falls back to Cwd.
	ProjectRoot string

	// Cwd is the hook input's cwd (= Claude Code's originalCwd at the tool call).
	Cwd string

	// ManagedOverride, when non-nil, replaces OS-default managed settings/CLAUDE.md paths — test seam to suppress reads
	// from the host's real managed settings.
	ManagedOverride []string
}

// Resolve builds a Paths value from loaded runtime config and the hook input. CLAUDE_PROJECT_DIR is the only env var
// consulted — it's set fresh per invocation and isn't part of static config.
func Resolve(runtimeClaudeConfigDir, cwd string) Paths {
	configDir := normalizeConfigDir(runtimeClaudeConfigDir)

	projectRoot := os.Getenv("CLAUDE_PROJECT_DIR")
	if projectRoot == "" {
		projectRoot = cwd
	}

	return Paths{
		ConfigDir:   configDir,
		ProjectRoot: projectRoot,
		Cwd:         cwd,
	}
}

// UserSettingsFile returns $CLAUDE_CONFIG_DIR/settings.json. Claude Code does not walk ancestors for this file.
func (p Paths) UserSettingsFile() string {
	if p.ConfigDir == "" {
		return ""
	}
	return filepath.Join(p.ConfigDir, "settings.json")
}

// ProjectSettingsFiles returns .claude/settings.json and .claude/settings.local.json anchored at cwd. Claude Code roots
// these at originalCwd (not the project root) and does not walk ancestors.
func (p Paths) ProjectSettingsFiles(cwd string) []string {
	if cwd == "" {
		return nil
	}
	return []string{
		filepath.Join(cwd, ".claude", "settings.json"),
		filepath.Join(cwd, ".claude", "settings.local.json"),
	}
}

// ManagedSettingsFiles returns the OS-specific managed-settings paths. Returns ManagedOverride if non-nil (test seam).
func (p Paths) ManagedSettingsFiles() []string {
	if p.ManagedOverride != nil {
		return p.ManagedOverride
	}
	switch runtime.GOOS {
	case "darwin":
		return []string{"/Library/Application Support/ClaudeCode/managed-settings.json"}
	case "linux":
		return []string{"/etc/claude-code/managed-settings.json"}
	}
	return nil
}

// AllSettingsFiles returns managed + user + project settings paths in precedence order (managed lowest, local highest).
// Callers union across all — Claude Code does not short-circuit on first hit.
func (p Paths) AllSettingsFiles(cwd string) []string {
	var out []string
	out = append(out, p.ManagedSettingsFiles()...)
	if f := p.UserSettingsFile(); f != "" {
		out = append(out, f)
	}
	out = append(out, p.ProjectSettingsFiles(cwd)...)
	return out
}

// UserClaudeMdFile returns the user-tier CLAUDE.md path ($CLAUDE_CONFIG_DIR/CLAUDE.md).
func (p Paths) UserClaudeMdFile() string {
	if p.ConfigDir == "" {
		return ""
	}
	return filepath.Join(p.ConfigDir, "CLAUDE.md")
}

// ManagedClaudeMdFiles returns the OS-specific managed CLAUDE.md paths. Shares the ManagedOverride seam with
// ManagedSettingsFiles — tests that suppress one typically suppress both.
func (p Paths) ManagedClaudeMdFiles() []string {
	if p.ManagedOverride != nil {
		return p.ManagedOverride
	}
	switch runtime.GOOS {
	case "darwin":
		return []string{"/Library/Application Support/ClaudeCode/CLAUDE.md"}
	case "linux":
		return []string{"/etc/claude-code/CLAUDE.md"}
	}
	return nil
}

// normalizeConfigDir matches Claude Code's getClaudeConfigHomeDir: tilde-expand then NFC-normalize.
func normalizeConfigDir(raw string) string {
	if raw == "" {
		return ""
	}
	expanded := pathutil.ExpandTilde(raw)
	return nfcNormalize(expanded)
}

// nfcNormalize applies NFC normalization so composed/decomposed variants of the same path don't diverge — macOS HFS+
// returns NFD-decomposed names from the kernel.
func nfcNormalize(s string) string {
	if isASCII(s) {
		return s
	}
	return norm.NFC.String(s)
}

func isASCII(s string) bool {
	for _, r := range s {
		if r > unicode.MaxASCII {
			return false
		}
	}
	return true
}
