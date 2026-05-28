// Package permscope resolves the permission scope a Claude Code session enforces, by reading the standard settings
// hierarchy (managed → user → project → project-local) and unioning two fields across every reachable file:
//
//   - `WorkingDirs`: cwd ∪ `permissions.additionalDirectories`.
//   - `DenyRules`: every `permissions.deny` pattern.
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
	// WorkingDirs is `cwd ∪ additionalDirectories`, tilde-expanded, symlink-resolved, and deduped. Cwd is always first.
	WorkingDirs []string

	// DenyRules is the union of `permissions.deny` patterns across settings tiers, sorted and deduped.
	DenyRules []string

	// Sources lists the settings files that actually contributed (existed on disk and parsed cleanly).
	Sources []string

	// Candidates is every settings path the resolver looked for, in hierarchy order (managed → user → project →
	// project-local), regardless of whether the file existed. Consumers that need to fingerprint the entire settings
	// surface (e.g. the auto-mode policy cache key) read this rather than `Sources`.
	Candidates []string
}

// Resolve returns the permission scope for the given Claude Code paths. Stateless — no resolver instance needed.
func Resolve(p paths.Paths) (Resolution, error) {
	candidates := p.AllSettingsFiles(p.Cwd)
	res, err := buildResolution(p.Cwd, existingFiles(candidates))
	if err != nil {
		return Resolution{}, err
	}
	res.Candidates = candidates
	return res, nil
}

func buildResolution(cwd string, sources []string) (Resolution, error) {
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
		additional = append(additional, parsed.Permissions.AdditionalDirectories...)
		denyRules = append(denyRules, parsed.Permissions.Deny...)
		contributing = append(contributing, p)
	}

	return Resolution{
		WorkingDirs: dedupeWorkingDirs(cwd, additional),
		DenyRules:   dedupeStrings(denyRules),
		Sources:     contributing,
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
