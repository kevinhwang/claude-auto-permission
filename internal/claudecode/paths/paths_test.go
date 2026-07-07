package paths

import (
	"path/filepath"
	"testing"
)

func TestResolve_ConfigDir_ExpandsTilde(t *testing.T) {
	t.Setenv("HOME", "/home/testuser")
	t.Setenv("CLAUDE_PROJECT_DIR", "")

	p := Resolve("~/.claude", "/project")
	want := "/home/testuser/.claude"
	if p.ConfigDir != want {
		t.Errorf("ConfigDir = %q, want %q", p.ConfigDir, want)
	}
}

func TestResolve_ConfigDir_AbsolutePassthrough(t *testing.T) {
	t.Setenv("CLAUDE_PROJECT_DIR", "")

	p := Resolve("/custom/config", "/project")
	if p.ConfigDir != "/custom/config" {
		t.Errorf("ConfigDir = %q, want /custom/config", p.ConfigDir)
	}
}

func TestResolve_ProjectRoot_FromEnv(t *testing.T) {
	t.Setenv("CLAUDE_PROJECT_DIR", "/stable/root")

	p := Resolve("/config", "/current/cwd")
	if p.ProjectRoot != "/stable/root" {
		t.Errorf("ProjectRoot = %q, want /stable/root", p.ProjectRoot)
	}
	if p.Cwd != "/current/cwd" {
		t.Errorf("Cwd = %q, want /current/cwd", p.Cwd)
	}
}

func TestResolve_ProjectRoot_FallbackToCwd(t *testing.T) {
	t.Setenv("CLAUDE_PROJECT_DIR", "")

	p := Resolve("/config", "/my/project")
	if p.ProjectRoot != "/my/project" {
		t.Errorf("ProjectRoot = %q, want /my/project (fallback to cwd)", p.ProjectRoot)
	}
}

func TestResolve_NFCNormalization(t *testing.T) {
	// NFD-decomposed ü = u + combining diaeresis
	nfd := "/home/üser/.claude"
	// NFC-composed ü
	nfc := "/home/üser/.claude"

	t.Setenv("CLAUDE_PROJECT_DIR", "")

	p := Resolve(nfd, "/cwd")
	if p.ConfigDir != nfc {
		t.Errorf("ConfigDir = %q, want NFC %q", p.ConfigDir, nfc)
	}
}

func TestUserSettingsFile(t *testing.T) {
	p := Paths{ConfigDir: "/home/user/.claude"}
	got := p.UserSettingsFile()
	want := "/home/user/.claude/settings.json"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestProjectSettingsFiles(t *testing.T) {
	p := Paths{Cwd: "/project"}
	got := p.ProjectSettingsFiles("/project")
	if len(got) != 2 {
		t.Fatalf("got %d files, want 2", len(got))
	}
	if got[0] != filepath.Join("/project", ".claude", "settings.json") {
		t.Errorf("[0] = %q", got[0])
	}
	if got[1] != filepath.Join("/project", ".claude", "settings.local.json") {
		t.Errorf("[1] = %q", got[1])
	}
}

func TestProjectSettingsFiles_EmptyCwd(t *testing.T) {
	p := Paths{}
	if got := p.ProjectSettingsFiles(""); got != nil {
		t.Errorf("expected nil for empty cwd, got %v", got)
	}
}

func TestAllSettingsFiles_Order(t *testing.T) {
	p := Paths{ConfigDir: "/home/user/.claude", Cwd: "/project"}
	files := p.AllSettingsFiles("/project")

	// Should be: managed... + user + project + local
	if len(files) < 3 {
		t.Fatalf("got %d files, want >= 3", len(files))
	}
	// User is after managed, before project.
	foundUser := false
	for _, f := range files {
		if f == "/home/user/.claude/settings.json" {
			foundUser = true
		}
	}
	if !foundUser {
		t.Errorf("user settings not in list: %v", files)
	}
}

func TestUserClaudeMdFile(t *testing.T) {
	p := Paths{ConfigDir: "/custom/dir"}
	got := p.UserClaudeMdFile()
	if got != "/custom/dir/CLAUDE.md" {
		t.Errorf("got %q, want /custom/dir/CLAUDE.md", got)
	}
}

func TestUserClaudeMdFile_EmptyConfigDir(t *testing.T) {
	p := Paths{}
	if got := p.UserClaudeMdFile(); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}
