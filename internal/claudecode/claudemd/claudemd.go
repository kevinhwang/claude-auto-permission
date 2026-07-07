// Package claudemd walks the CLAUDE.md tree the way Claude Code itself does, resolving `@-imports` recursively and
// returning the per-file bodies as plain content. Framing decisions (how to label sections, what preamble to attach)
// belong to whichever consumer renders the result; this package returns raw material only.
//
// Walk order, low → high priority (later entries override earlier ones in any consumer that resolves conflicts):
//
//  1. Managed (OS-specific) CLAUDE.md.
//  2. User: `$CLAUDE_CONFIG_DIR/CLAUDE.md`.
//  3. Each ancestor of cwd from filesystem root down to cwd inclusive, contributing `CLAUDE.md`, `.claude/CLAUDE.md`,
//     and `CLAUDE.local.md` at every level.
//  4. `.claude/rules/*.md` at every ancestor of cwd.
//
// `@-imports` resolve recursively up to [MaxIncludeDepth] with a cycle detector as the primary guard.
package claudemd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"claude-auto-permission/internal/pathutil"
)

// MaxIncludeDepth backstops the cycle detector; deeper imports drop.
const MaxIncludeDepth = 5

// Section is one CLAUDE.md file's resolved content. `Path` is the resolved absolute path on disk; `Content` is the body
// with all `@-imports` inlined.
type Section struct {
	Path    string
	Content string
}

// Bundle is the assembled walk output. `Sections` is in walk order (lowest to highest priority); `Sources` lists every
// file that contributed (parent files plus their inlined imports), in the order they were read.
type Bundle struct {
	Sections []Section
	Sources  []string
}

// Loader walks the CLAUDE.md tree and resolves `@-imports`.
type Loader struct {
	// ConfigDir is Claude Code's user-tier config directory (typically `~/.claude`). Empty disables the user-level
	// CLAUDE.md.
	ConfigDir string

	// ManagedPaths overrides the OS-default managed paths. `nil` uses the OS default; an empty slice suppresses managed
	// reads (test seam).
	ManagedPaths []string
}

// Load assembles the CLAUDE.md tree for cwd. Missing files are tolerated; only outright filesystem errors return an
// error.
func (l *Loader) Load(cwd string) (Bundle, error) {
	roots := l.candidateRoots(cwd)
	return l.build(cwd, roots)
}

// candidateRoots is the ordered file list to walk before the `.claude/rules/*.md` glob.
func (l *Loader) candidateRoots(cwd string) []string {
	var paths []string

	managed := l.ManagedPaths
	if managed == nil {
		managed = managedPaths()
	}
	paths = append(paths, managed...)

	if l.ConfigDir != "" {
		paths = append(paths, filepath.Join(l.ConfigDir, "CLAUDE.md"))
	}

	if cwd != "" {
		for _, dir := range ancestorsRootDown(cwd) {
			paths = append(paths,
				filepath.Join(dir, "CLAUDE.md"),
				filepath.Join(dir, ".claude", "CLAUDE.md"),
				filepath.Join(dir, "CLAUDE.local.md"),
			)
		}
	}

	return paths
}

// ancestorsRootDown returns each ancestor of cwd from filesystem root down to cwd, inclusive. Files closer to cwd take
// priority and so appear last.
func ancestorsRootDown(cwd string) []string {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return []string{cwd}
	}
	var dirs []string
	for d := abs; ; d = filepath.Dir(d) {
		dirs = append([]string{d}, dirs...)
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
	}
	return dirs
}

// build reads each candidate root, then sweeps `.claude/rules/*.md`, resolving `@-imports` and accumulating Sections in
// walk order.
func (l *Loader) build(cwd string, roots []string) (Bundle, error) {
	var b Bundle
	visited := newCycleSet()

	for _, p := range roots {
		text, sources, err := readWithImports(p, visited, 0)
		if err != nil {
			return Bundle{}, err
		}
		if text != "" {
			b.Sections = append(b.Sections, Section{Path: sources[0], Content: text})
			b.Sources = append(b.Sources, sources...)
		}
	}

	// `.claude/rules/*.md` is a directory listing rather than a fixed name, so it's walked separately from the canonical
	// roots.
	if cwd != "" {
		for _, dir := range ancestorsRootDown(cwd) {
			rulesDir := filepath.Join(dir, ".claude", "rules")
			entries, err := os.ReadDir(rulesDir)
			if err != nil {
				continue
			}
			for _, e := range entries {
				if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
					continue
				}
				p := filepath.Join(rulesDir, e.Name())
				text, sources, err := readWithImports(p, visited, 0)
				if err != nil {
					return Bundle{}, err
				}
				if text != "" {
					b.Sections = append(b.Sections, Section{Path: sources[0], Content: text})
					b.Sources = append(b.Sources, sources...)
				}
			}
		}
	}

	return b, nil
}

// importRegex matches `@path` references after a whitespace boundary. The path may contain backslash-escaped spaces.
// The leading `\s` is matched but only the path itself is captured.
var importRegex = regexp.MustCompile(`(?:^|\s)@((?:[^\s\\]|\\ )+)`)

// codeFenceRegex and inlineCodeRegex match code fences and inline spans so an `@directive` shown inside an example
// isn't interpreted as a real import.
var (
	codeFenceRegex  = regexp.MustCompile("```[\\s\\S]*?```")
	inlineCodeRegex = regexp.MustCompile("`[^`\n]*`")
)

// readWithImports reads `path`, recursively resolves `@-imports` up to [MaxIncludeDepth], and returns the assembled
// text plus the list of contributing files (parent first, imports in walk order).
func readWithImports(path string, visited *cycleSet, depth int) (text string, sources []string, err error) {
	if depth > MaxIncludeDepth {
		return "", nil, nil
	}

	// Resolve symlinks so two aliases for the same file collapse to one cycle-set entry. Any resolve error is treated as a
	// missing file — the walker probes many paths optimistically.
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", nil, nil
	}
	if visited.contains(resolved) {
		return "", nil, nil
	}
	visited.add(resolved, path)

	data, err := os.ReadFile(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil, nil
		}
		return "", nil, fmt.Errorf("read %s: %w", resolved, err)
	}
	body := string(data)
	sources = append(sources, resolved)

	// stripCodeRegions only blanks out characters; it never shifts positions, so byte indices map 1:1 between `stripped`
	// and `body`.
	stripped := stripCodeRegions(body)
	matches := importRegex.FindAllStringSubmatchIndex(stripped, -1)

	if len(matches) == 0 {
		return body, sources, nil
	}

	var out strings.Builder
	cursor := 0
	for _, m := range matches {
		// m[0..1] = full match including any leading boundary char; m[2..3] = captured path.
		fullStart, fullEnd := m[0], m[1]
		pathStart, pathEnd := m[2], m[3]
		ref := stripped[pathStart:pathEnd]

		out.WriteString(body[cursor:fullStart])
		// Preserve the leading whitespace so neighboring tokens don't fuse.
		if fullStart < pathStart-1 {
			out.WriteString(body[fullStart : pathStart-1])
		}

		importPath := resolveImportPath(ref, resolved)
		importedText, importedSources, ierr := readWithImports(importPath, visited, depth+1)
		if ierr != nil {
			return "", nil, ierr
		}
		if importedText != "" {
			out.WriteString("\n")
			out.WriteString(importedText)
			out.WriteString("\n")
		}
		sources = append(sources, importedSources...)
		cursor = fullEnd
	}
	out.WriteString(body[cursor:])

	return out.String(), sources, nil
}

// stripCodeRegions replaces characters inside code fences and inline spans with spaces so the import regex skips them.
// Output length equals input length — every byte either passes through or becomes a space — so indices align with the
// original `body`.
func stripCodeRegions(s string) string {
	stripped := []byte(s)
	for _, m := range codeFenceRegex.FindAllStringIndex(s, -1) {
		for i := m[0]; i < m[1]; i++ {
			if stripped[i] != '\n' {
				stripped[i] = ' '
			}
		}
	}
	for _, m := range inlineCodeRegex.FindAllStringIndex(s, -1) {
		for i := m[0]; i < m[1]; i++ {
			if stripped[i] != '\n' {
				stripped[i] = ' '
			}
		}
	}
	return string(stripped)
}

// resolveImportPath resolves an `@-import` reference relative to the importing file. Supports `@~/foo`, `@/abs`,
// `@./rel`, and an optional `@file#section` fragment which is stripped.
func resolveImportPath(ref, importerPath string) string {
	if hashIdx := strings.Index(ref, "#"); hashIdx >= 0 {
		ref = ref[:hashIdx]
	}
	ref = strings.ReplaceAll(ref, `\ `, " ")

	switch {
	case strings.HasPrefix(ref, "~/") || ref == "~":
		return pathutil.ExpandTilde(ref)
	case strings.HasPrefix(ref, "/"):
		return ref
	default:
		return filepath.Join(filepath.Dir(importerPath), ref)
	}
}

// cycleSet tracks paths already inlined. Both the resolved physical path and the original alias are recorded so the
// same file can't be visited twice via different symlinks.
type cycleSet struct {
	seen map[string]bool
}

func newCycleSet() *cycleSet {
	return &cycleSet{seen: make(map[string]bool)}
}

func (c *cycleSet) contains(resolved string) bool {
	return c.seen[resolved]
}

func (c *cycleSet) add(resolved, original string) {
	c.seen[resolved] = true
	if original != resolved {
		c.seen[original] = true
	}
}

// managedPaths returns the OS-specific managed CLAUDE.md location, or nil if the OS has no convention.
func managedPaths() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"/Library/Application Support/ClaudeCode/CLAUDE.md"}
	case "linux":
		return []string{"/etc/claude-code/CLAUDE.md"}
	}
	return nil
}
