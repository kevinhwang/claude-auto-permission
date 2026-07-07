// Package permscope resolves the permission scope a Claude Code session enforces, by reading the standard settings
// hierarchy (managed → user → project → project-local) and unioning two fields across reachable files:
//
//   - `TrustedWorkingDirs`: cwd ∪ `permissions.additionalDirectories`, but only from tiers a hostile repo can't
//     control. The project-tier `<cwd>/.claude/settings.json` is excluded: it's checked into the repo, so an
//     attacker could otherwise list `~/.aws` / `/etc` there and have those directories treated as in-scope by the
//     classifier (silencing the prompt's scrutiny and the file-op skip-list). The git-ignored
//     `settings.local.json` is NOT repo-controlled and stays trusted, matching Claude Code's own per-field
//     `projectSettings` exclusions for security-sensitive settings.
//   - `DenyRules`: every `permissions.deny` pattern, from all tiers including the project file. Extra deny patterns
//     from a hostile repo can only ever block more, never widen access, so there's nothing to exclude.
//
// Session-scoped `addDirectories` set mid-session via `/add-dir` live only in Claude Code's in-memory state and never
// reach disk — calls in those directories will classify until the user persists them to settings.local.json.
package permscope

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"claude-auto-permission/internal/claudecode/paths"
	"claude-auto-permission/internal/pathutil"
)

// Resolution is the resolved permission scope for one invocation.
type Resolution struct {
	// TrustedWorkingDirs is `cwd ∪ additionalDirectories` (tilde-expanded, symlink-resolved, deduped, cwd first),
	// drawn only from tiers a hostile repo can't control — the project-tier `<cwd>/.claude/settings.json` is
	// excluded. See the package doc for why. Cwd itself is included for the file-op skip-list (an in-cwd read is a
	// non-event); the classifier prompt separately tells the model not to treat cwd as an authorization.
	TrustedWorkingDirs []string

	// DenyRules is the union of `permissions.deny` patterns across all settings tiers, sorted and deduped.
	DenyRules []string

	// Sources lists the settings files that actually contributed (existed on disk and parsed cleanly).
	Sources []string

	// Candidates is every settings path the resolver looked for, in hierarchy order (managed → user → project →
	// project-local), regardless of whether the file existed. Consumers that need to fingerprint the entire settings
	// surface (e.g. the auto-mode policy cache key) read this rather than `Sources`.
	Candidates []string
}

// Resolve returns the permission scope for the given Claude Code paths. Stateless — no resolver instance needed.
//
// Project-tier settings files are anchored at the project root, not the live cwd: Claude Code resolves its own
// settings from a stable launch dir that doesn't follow the agent's `cd`s, so anchoring at the drifting cwd would read
// a different project's settings than the session actually uses. The root falls back to cwd when unset.
func Resolve(p paths.Paths) (Resolution, error) {
	root := p.ProjectRoot
	if root == "" {
		root = p.Cwd
	}
	candidates := p.AllSettingsFiles(root)
	// The repo-controlled project file whose additionalDirectories we must not trust (see package doc).
	// ProjectSettingsFiles returns [settings.json, settings.local.json]; only the former is checked into the repo.
	var repoProjectFile string
	if projectFiles := p.ProjectSettingsFiles(root); len(projectFiles) > 0 {
		repoProjectFile = projectFiles[0]
	}
	res, err := buildResolution(p.Cwd, existingFiles(candidates), repoProjectFile)
	if err != nil {
		return Resolution{}, err
	}
	res.Candidates = candidates
	return res, nil
}

// buildResolution unions deny rules from every source, but excludes additionalDirectories contributed by
// repoProjectFile (the repo-controlled `<cwd>/.claude/settings.json`) from the trusted working-dir set.
func buildResolution(cwd string, sources []string, repoProjectFile string) (Resolution, error) {
	var additional, denyRules []string
	var contributing []string

	for _, p := range sources {
		data, err := os.ReadFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return Resolution{}, fmt.Errorf("read %s: %w", p, err)
		}
		var parsed permissionsBlock
		if err := json.Unmarshal(data, &parsed); err != nil {
			return Resolution{}, fmt.Errorf("parse %s: %w", p, err)
		}
		if parsed.Permissions == nil {
			continue
		}
		// Deny rules are safe from every tier; additionalDirectories from the repo-controlled project file are not.
		if p != repoProjectFile {
			additional = append(additional, parsed.Permissions.AdditionalDirectories...)
		}
		denyRules = append(denyRules, parsed.Permissions.Deny...)
		contributing = append(contributing, p)
	}

	return Resolution{
		TrustedWorkingDirs: dedupeWorkingDirs(cwd, additional),
		DenyRules:          dedupeStrings(denyRules),
		Sources:            contributing,
	}, nil
}

type permissionsBlock struct {
	Permissions *permissions `json:"permissions"`
}

type permissions struct {
	AdditionalDirectories []string `json:"additionalDirectories"`
	Deny                  []string `json:"deny"`
}

func dedupeWorkingDirs(cwd string, additional []string) []string {
	seen := map[string]bool{}
	var out []string

	add := func(raw string) {
		if raw == "" {
			return
		}
		expanded := pathutil.ExpandTilde(raw)
		resolved := pathutil.RealPath(expanded)
		if seen[resolved] {
			return
		}
		seen[resolved] = true
		out = append(out, resolved)
	}

	add(cwd)
	sorted := append([]string{}, additional...)
	sort.Strings(sorted)
	for _, d := range sorted {
		add(d)
	}
	return out
}

func dedupeStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func existingFiles(paths []string) []string {
	var out []string
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	return out
}
