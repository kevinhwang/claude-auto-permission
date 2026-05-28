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

func TestResolve_UnionsAdditionalDirectoriesAcrossLevels(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()

	writeSettings(t, home, "settings.json", map[string]any{
		"additionalDirectories": []string{filepath.Join(home, "user-extra")},
	})
	writeSettings(t, cwd, "settings.json", map[string]any{
		"additionalDirectories": []string{filepath.Join(home, "project-extra")},
	})
	writeSettings(t, cwd, "settings.local.json", map[string]any{
		"additionalDirectories": []string{
			filepath.Join(home, "local-extra"),
			filepath.Join(home, "user-extra"), // duplicate
		},
	})

	for _, sub := range []string{"user-extra", "project-extra", "local-extra"} {
		if err := os.MkdirAll(filepath.Join(home, sub), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	paths := makePaths(t, home, cwd)
	got, err := Resolve(paths)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if len(got.WorkingDirs) == 0 || got.WorkingDirs[0] != realpath(cwd) {
		t.Errorf("cwd not first; got %v", got.WorkingDirs)
	}
	wantDirs := []string{
		realpath(cwd),
		realpath(filepath.Join(home, "local-extra")),
		realpath(filepath.Join(home, "project-extra")),
		realpath(filepath.Join(home, "user-extra")),
	}
	if !reflect.DeepEqual(got.WorkingDirs, wantDirs) {
		t.Errorf("WorkingDirs = %v\nwant %v", got.WorkingDirs, wantDirs)
	}
	if len(got.Sources) != 3 {
		t.Errorf("Sources = %v; want 3 entries", got.Sources)
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
	if !reflect.DeepEqual(got.WorkingDirs, []string{realpath(cwd)}) {
		t.Errorf("WorkingDirs = %v; want just cwd", got.WorkingDirs)
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
	if !slices.Contains(got.WorkingDirs, realpath(target)) {
		t.Errorf("expected %s in %v", target, got.WorkingDirs)
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
	if reflect.DeepEqual(first.WorkingDirs, second.WorkingDirs) {
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
