package claudemd

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func loaderForTest(t *testing.T, home string) *Loader {
	t.Helper()
	return &Loader{
		ConfigDir:    filepath.Join(home, ".claude"),
		ManagedPaths: []string{},
	}
}

// joinedContent concatenates every Section.Content with a separator, matching how a typical consumer would render the
// bundle.
func joinedContent(b Bundle) string {
	parts := make([]string, 0, len(b.Sections))
	for _, s := range b.Sections {
		parts = append(parts, s.Content)
	}
	return strings.Join(parts, "\n\n")
}

func TestLoad_NoFilesIsEmpty(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	res, err := loaderForTest(t, home).Load(cwd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(res.Sections) != 0 {
		t.Errorf("Sections = %v, want empty", res.Sections)
	}
	if len(res.Sources) != 0 {
		t.Errorf("Sources = %v, want empty", res.Sources)
	}
}

func TestLoad_UserAndProjectAssembledInOrder(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()

	writeFile(t, filepath.Join(home, ".claude", "CLAUDE.md"), "# user-level\nfoo")
	writeFile(t, filepath.Join(cwd, "CLAUDE.md"), "# project-level\nbar")

	res, err := loaderForTest(t, home).Load(cwd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	joined := joinedContent(res)
	if !strings.Contains(joined, "# user-level") || !strings.Contains(joined, "# project-level") {
		t.Errorf("missing expected content:\n%s", joined)
	}
	// User comes before project (lower priority first).
	userIdx := strings.Index(joined, "# user-level")
	projIdx := strings.Index(joined, "# project-level")
	if userIdx < 0 || projIdx < 0 || userIdx > projIdx {
		t.Errorf("ordering wrong; user=%d project=%d", userIdx, projIdx)
	}
}

func TestLoad_AtImport_ResolvedAndInlined(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()

	// CLAUDE.md → @AGENTS.md (the pattern this project uses).
	writeFile(t, filepath.Join(cwd, "CLAUDE.md"), "@AGENTS.md\n\nMore project content.")
	writeFile(t, filepath.Join(cwd, "AGENTS.md"), "AGENTS-content body")

	res, err := loaderForTest(t, home).Load(cwd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !strings.Contains(joinedContent(res), "AGENTS-content body") {
		t.Errorf("AGENTS.md not inlined: %s", joinedContent(res))
	}
	if len(res.Sources) < 2 {
		t.Errorf("Sources = %v, want >= 2 entries", res.Sources)
	}
}

func TestLoad_AtImport_AbsolutePath(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	other := filepath.Join(t.TempDir(), "absolute.md")
	writeFile(t, other, "absolute body")
	writeFile(t, filepath.Join(cwd, "CLAUDE.md"), "@"+other+"\n")

	res, err := loaderForTest(t, home).Load(cwd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !strings.Contains(joinedContent(res), "absolute body") {
		t.Errorf("absolute import not inlined: %s", joinedContent(res))
	}
}

func TestLoad_AtImport_TildeExpansion(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeFile(t, filepath.Join(home, "extra.md"), "tilde body")
	writeFile(t, filepath.Join(cwd, "CLAUDE.md"), "@~/extra.md\n")

	t.Setenv("HOME", home)
	res, err := loaderForTest(t, home).Load(cwd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !strings.Contains(joinedContent(res), "tilde body") {
		t.Errorf("~ import not expanded: %s", joinedContent(res))
	}
}

func TestLoad_AtImport_FragmentStripped(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeFile(t, filepath.Join(cwd, "CLAUDE.md"), "@notes.md#section\n")
	writeFile(t, filepath.Join(cwd, "notes.md"), "notes body")

	res, err := loaderForTest(t, home).Load(cwd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !strings.Contains(joinedContent(res), "notes body") {
		t.Errorf("fragment-stripped import not resolved: %s", joinedContent(res))
	}
}

// `@INSIDE-CODE.md` inside a fenced code block is example text, not an import directive. The walker must preserve it
// verbatim.
func TestLoad_AtImport_SkipsCodeBlocks(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeFile(t, filepath.Join(cwd, "CLAUDE.md"), "Real content\n\n```\n@INSIDE-CODE.md\n```\n\nReal trailer")
	// Deliberately don't create INSIDE-CODE.md — if the regex matched inside the fence, the import would silently no-op
	// and the literal text would still drop out.

	res, err := loaderForTest(t, home).Load(cwd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !strings.Contains(joinedContent(res), "@INSIDE-CODE.md") {
		t.Errorf("@INSIDE-CODE.md was rewritten — code fence not respected:\n%s", joinedContent(res))
	}
}

func TestLoad_AtImport_CycleProtection(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	a := filepath.Join(cwd, "CLAUDE.md")
	b := filepath.Join(cwd, "B.md")
	writeFile(t, a, "@B.md\nA-body")
	writeFile(t, b, "@CLAUDE.md\nB-body")

	res, err := loaderForTest(t, home).Load(cwd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	joined := joinedContent(res)
	if !strings.Contains(joined, "A-body") || !strings.Contains(joined, "B-body") {
		t.Errorf("expected both bodies; got: %s", joined)
	}
}

func TestLoad_AtImport_DepthLimit(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()

	// Build CLAUDE.md → 1.md → 2.md → … → 10.md. With MaxIncludeDepth=5, content past depth 5 won't be inlined.
	writeFile(t, filepath.Join(cwd, "CLAUDE.md"), "@1.md\n")
	for i := 1; i <= 9; i++ {
		body := "level" + strconv.Itoa(i) + " "
		body += "@" + filepath.Join(cwd, strconv.Itoa(i+1)+".md") + "\n"
		writeFile(t, filepath.Join(cwd, strconv.Itoa(i)+".md"), body)
	}
	writeFile(t, filepath.Join(cwd, "1.md"), "level1 @"+filepath.Join(cwd, "2.md")+"\n")
	writeFile(t, filepath.Join(cwd, "10.md"), "level10")

	res, err := loaderForTest(t, home).Load(cwd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	joined := joinedContent(res)
	if strings.Contains(joined, "level10") {
		t.Errorf("depth limit not enforced; got level10 inlined")
	}
	if !strings.Contains(joined, "level1") {
		t.Errorf("expected at least level1 inlined: %s", joined)
	}
}

func TestLoad_AncestorWalkRootDown(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	mid := filepath.Join(root, "mid")
	deep := filepath.Join(mid, "deep")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	writeFile(t, filepath.Join(root, "CLAUDE.md"), "ROOT-claude")
	writeFile(t, filepath.Join(mid, "CLAUDE.md"), "MID-claude")
	writeFile(t, filepath.Join(deep, "CLAUDE.md"), "DEEP-claude")

	res, err := loaderForTest(t, home).Load(deep)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	joined := joinedContent(res)
	rootIdx := strings.Index(joined, "ROOT-claude")
	midIdx := strings.Index(joined, "MID-claude")
	deepIdx := strings.Index(joined, "DEEP-claude")
	if rootIdx < 0 || midIdx < 0 || deepIdx < 0 {
		t.Errorf("missing entries; root=%d mid=%d deep=%d\n%s", rootIdx, midIdx, deepIdx, joined)
	}
	if !(rootIdx < midIdx && midIdx < deepIdx) {
		t.Errorf("ordering not root→deep; root=%d mid=%d deep=%d", rootIdx, midIdx, deepIdx)
	}
}

func TestLoad_LocalMd_Included(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeFile(t, filepath.Join(cwd, "CLAUDE.local.md"), "local-only content")

	res, err := loaderForTest(t, home).Load(cwd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !strings.Contains(joinedContent(res), "local-only content") {
		t.Errorf("CLAUDE.local.md not included: %s", joinedContent(res))
	}
}

func TestLoad_RulesGlob_Included(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeFile(t, filepath.Join(cwd, ".claude", "rules", "01-style.md"), "style rules")
	writeFile(t, filepath.Join(cwd, ".claude", "rules", "02-tests.md"), "test rules")

	res, err := loaderForTest(t, home).Load(cwd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	joined := joinedContent(res)
	for _, want := range []string{"style rules", "test rules"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q from %s", want, joined)
		}
	}
}

// Sections expose Path so consumers can render per-file framing.
func TestLoad_SectionsCarryAbsolutePath(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	writeFile(t, filepath.Join(cwd, "CLAUDE.md"), "anything")

	res, err := loaderForTest(t, home).Load(cwd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(res.Sections) == 0 {
		t.Fatal("expected at least one section")
	}
	if !filepath.IsAbs(res.Sections[0].Path) {
		t.Errorf("Section.Path %q is not absolute", res.Sections[0].Path)
	}
}
