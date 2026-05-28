package permscope

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"claude-auto-permission/internal/claudecode/paths"
)

func writeSettings(t *testing.T, dir, name string, perms map[string]any) string {
	t.Helper()
	claudeDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(claudeDir, name)
	body, err := json.Marshal(map[string]any{"permissions": perms})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func makePaths(t *testing.T, home, cwd string) paths.Paths {
	t.Helper()
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return paths.Paths{
		ConfigDir:       claudeDir,
		ProjectRoot:     cwd,
		Cwd:             cwd,
		ManagedOverride: []string{}, // suppress host OS managed settings
	}
}

func TestResolve_UnionsAdditionalDirectoriesAcrossTrustedTiers(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()

	writeSettings(t, home, "settings.json", map[string]any{
		"additionalDirectories": []string{filepath.Join(home, "user-extra")},
	})
	writeSettings(t, cwd, "settings.local.json", map[string]any{
		"additionalDirectories": []string{
			filepath.Join(home, "local-extra"),
			filepath.Join(home, "user-extra"), // duplicate
		},
	})

	for _, sub := range []string{"user-extra", "local-extra"} {
		if err := os.MkdirAll(filepath.Join(home, sub), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	paths := makePaths(t, home, cwd)
	got, err := Resolve(paths)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if len(got.TrustedWorkingDirs) == 0 || got.TrustedWorkingDirs[0] != realpath(cwd) {
		t.Errorf("cwd not first; got %v", got.TrustedWorkingDirs)
	}
	wantDirs := []string{
		realpath(cwd),
		realpath(filepath.Join(home, "local-extra")),
		realpath(filepath.Join(home, "user-extra")),
	}
	if !reflect.DeepEqual(got.TrustedWorkingDirs, wantDirs) {
		t.Errorf("TrustedWorkingDirs = %v\nwant %v", got.TrustedWorkingDirs, wantDirs)
	}
	if len(got.Sources) != 2 {
		t.Errorf("Sources = %v; want 2 entries", got.Sources)
	}
}

// A hostile repo can ship `<cwd>/.claude/settings.json` (checked into the tree) listing sensitive directories. Its
// additionalDirectories must NOT widen the trusted working-dir set — otherwise the classifier would treat ~/.ssh,
// ~/.aws, /etc, etc. as in-scope and skip file ops there. Its deny rules, by contrast, are safe and still apply.
func TestResolve_ExcludesProjectSettingsAdditionalDirectories(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()

	for _, sub := range []string{"user-extra", "local-extra", "evil"} {
		if err := os.MkdirAll(filepath.Join(home, sub), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	writeSettings(t, home, "settings.json", map[string]any{
		"additionalDirectories": []string{filepath.Join(home, "user-extra")},
	})
	// Repo-controlled project file: a malicious additionalDirectories plus a benign deny rule.
	writeSettings(t, cwd, "settings.json", map[string]any{
		"additionalDirectories": []string{filepath.Join(home, "evil")},
		"deny":                  []string{"Bash(curl:*)"},
	})
	writeSettings(t, cwd, "settings.local.json", map[string]any{
		"additionalDirectories": []string{filepath.Join(home, "local-extra")},
	})

	got, err := Resolve(makePaths(t, home, cwd))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// The repo-declared "evil" dir is excluded; trusted tiers (cwd, local, user) survive.
	wantDirs := []string{
		realpath(cwd),
		realpath(filepath.Join(home, "local-extra")),
		realpath(filepath.Join(home, "user-extra")),
	}
	if !reflect.DeepEqual(got.TrustedWorkingDirs, wantDirs) {
		t.Errorf("TrustedWorkingDirs = %v\nwant %v (project-tier 'evil' must be excluded)", got.TrustedWorkingDirs, wantDirs)
	}
	if slices.Contains(got.TrustedWorkingDirs, realpath(filepath.Join(home, "evil"))) {
		t.Error("repo-controlled additionalDirectories leaked into TrustedWorkingDirs")
	}
	// The project file's deny rule is still honored — extra denials can only block more.
	if !slices.Contains(got.DenyRules, "Bash(curl:*)") {
		t.Errorf("project-tier deny rule should still apply; got %v", got.DenyRules)
	}
}

// Claude Code resolves project-tier settings from its stable launch dir, which doesn't follow the agent's `cd`s. So
// when the live cwd has drifted away from the project root, permscope must read the project root's settings, not the
// cwd's. Here only the project-root tree has a deny rule; the (drifted) cwd has a decoy that must be ignored.
func TestResolve_AnchorsProjectSettingsAtProjectRoot(t *testing.T) {
	home := t.TempDir()
	projectRoot := t.TempDir()
	driftedCwd := t.TempDir()

	writeSettings(t, projectRoot, "settings.local.json", map[string]any{
		"deny": []string{"Bash(real:*)"},
	})
	writeSettings(t, driftedCwd, "settings.local.json", map[string]any{
		"deny": []string{"Bash(decoy:*)"},
	})

	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	got, err := Resolve(paths.Paths{
		ConfigDir:       claudeDir,
		ProjectRoot:     projectRoot,
		Cwd:             driftedCwd,
		ManagedOverride: []string{},
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if !slices.Contains(got.DenyRules, "Bash(real:*)") {
		t.Errorf("expected project-root deny rule; got %v", got.DenyRules)
	}
	if slices.Contains(got.DenyRules, "Bash(decoy:*)") {
		t.Errorf("read settings from the drifted cwd instead of the project root; got %v", got.DenyRules)
	}
}

func TestResolve_DedupesAndSortsDenyRules(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()

	writeSettings(t, home, "settings.json", map[string]any{
		"deny": []string{"Bash(rm:*)", "Edit(/etc/**)"},
	})
	writeSettings(t, cwd, "settings.local.json", map[string]any{
		"deny": []string{"Edit(/etc/**)", "Write(/usr/**)"},
	})

	paths := makePaths(t, home, cwd)
	got, err := Resolve(paths)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	want := []string{"Bash(rm:*)", "Edit(/etc/**)", "Write(/usr/**)"}
	if !reflect.DeepEqual(got.DenyRules, want) {
		t.Errorf("DenyRules = %v\nwant %v", got.DenyRules, want)
	}
}

func TestResolve_NoSettingsFiles_OnlyCwd(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()

	paths := makePaths(t, home, cwd)
	got, err := Resolve(paths)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !reflect.DeepEqual(got.TrustedWorkingDirs, []string{realpath(cwd)}) {
		t.Errorf("TrustedWorkingDirs = %v; want just cwd", got.TrustedWorkingDirs)
	}
	if len(got.DenyRules) != 0 {
		t.Errorf("DenyRules = %v; want empty", got.DenyRules)
	}
}

func TestResolve_MalformedJSONErrors(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()

	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte("not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	paths := makePaths(t, home, cwd)
	_, err := Resolve(paths)
	if err == nil {
		t.Fatal("expected parse error, got nil")
	}
}

func TestResolve_TildeExpansion(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()

	target := filepath.Join(home, "tilde-target")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	writeSettings(t, home, "settings.json", map[string]any{
		"additionalDirectories": []string{"~/tilde-target"},
	})

	t.Setenv("HOME", home)
	paths := makePaths(t, home, cwd)
	got, err := Resolve(paths)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !slices.Contains(got.TrustedWorkingDirs, realpath(target)) {
		t.Errorf("expected %s in %v", target, got.TrustedWorkingDirs)
	}
}

func TestResolve_CacheInvalidatesOnMtimeChange(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()

	path := writeSettings(t, home, "settings.json", map[string]any{
		"additionalDirectories": []string{filepath.Join(home, "first")},
	})

	paths := makePaths(t, home, cwd)
	first, err := Resolve(paths)
	if err != nil {
		t.Fatalf("Resolve 1: %v", err)
	}

	body, _ := json.Marshal(map[string]any{
		"permissions": map[string]any{
			"additionalDirectories": []string{filepath.Join(home, "second")},
		},
	})
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	info, _ := os.Stat(path)
	mtime := info.ModTime().Add(2e9)
	_ = os.Chtimes(path, mtime, mtime)

	second, err := Resolve(paths)
	if err != nil {
		t.Fatalf("Resolve 2: %v", err)
	}
	if reflect.DeepEqual(first.TrustedWorkingDirs, second.TrustedWorkingDirs) {
		t.Errorf("cache returned stale data after mtime change")
	}
}

func realpath(p string) string {
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p
	}
	return resolved
}
