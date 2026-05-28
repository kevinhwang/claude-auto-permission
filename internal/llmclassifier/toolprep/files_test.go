package toolprep

import (
	"encoding/json"
	"strings"
	"testing"
)

// ─── Read ──────────────────────────────────────────────────────────

func TestRead_SkipsCwdPaths(t *testing.T) {
	ev := NewRead()
	in := Input{
		ToolName:  "Read",
		ToolInput: json.RawMessage(`{"file_path":"/project/src/foo.go"}`),
		Cwd:       "/project",
	}
	if got, _ := ev.Skippable(in); got != Skip {
		t.Errorf("got %v, want Skip", got)
	}
}

func TestRead_ClassifiesOutOfCwdPaths(t *testing.T) {
	ev := NewRead()
	for _, path := range []string{"/etc/passwd", "/Users/foo/.aws/credentials", "/tmp/leaked.txt"} {
		t.Run(path, func(t *testing.T) {
			in := Input{
				ToolName:  "Read",
				ToolInput: json.RawMessage(`{"file_path":` + jsonString(path) + `}`),
				Cwd:       "/project",
			}
			if got, _ := ev.Skippable(in); got != SkipNone {
				t.Errorf("got %v, want SkipNone for %s", got, path)
			}
		})
	}
}

func TestRead_NoCwdMeansClassify(t *testing.T) {
	ev := NewRead()
	in := Input{
		ToolName:  "Read",
		ToolInput: json.RawMessage(`{"file_path":"/anywhere"}`),
		Cwd:       "",
	}
	if got, _ := ev.Skippable(in); got != SkipNone {
		t.Errorf("got %v, want SkipNone (cwd empty)", got)
	}
}

// Read consults the full WorkingDirs set, not just cwd, so paths in permissions.additionalDirectories also
// short-circuit.
func TestRead_SkipsAdditionalDirectories(t *testing.T) {
	ev := NewRead()
	in := Input{
		ToolName:    "Read",
		ToolInput:   json.RawMessage(`{"file_path":"/extra/sub/foo.go"}`),
		Cwd:         "/project",
		WorkingDirs: []string{"/project", "/extra"},
	}
	if got, _ := ev.Skippable(in); got != Skip {
		t.Errorf("got %v, want Skip", got)
	}
}

// Sanitize collapses the input to just the file path — that's the only field the reference impl exposes for Read.
// offset/limit don't affect the classifier's verdict (a malicious read is malicious regardless of which slice).
func TestRead_SanitizeCollapsesToFilePath(t *testing.T) {
	ev := NewRead()
	in := Input{
		ToolInput: json.RawMessage(`{"file_path":"/p/x","offset":100,"limit":50}`),
	}
	out, _ := ev.Sanitize(in)
	if got := string(out); got != `"/p/x"` {
		t.Errorf("got %q, want %q", got, `"/p/x"`)
	}
}

// ─── Write ─────────────────────────────────────────────────────────

// Write skips the classifier on in-scope writes so the orchestrator stays silent (no LLM call) and Claude Code's own
// permission state decides.
func TestWrite_SkipsInScope(t *testing.T) {
	ev := NewWrite()
	in := Input{
		ToolName:    "Write",
		ToolInput:   json.RawMessage(`{"file_path":"/project/foo.go","content":"package x"}`),
		Cwd:         "/project",
		WorkingDirs: []string{"/project"},
	}
	if got, _ := ev.Skippable(in); got != Skip {
		t.Errorf("got %v, want Skip", got)
	}
}

func TestWrite_OutOfScopeStillClassifies(t *testing.T) {
	ev := NewWrite()
	in := Input{
		ToolName:    "Write",
		ToolInput:   json.RawMessage(`{"file_path":"/etc/passwd","content":"root:..."}`),
		Cwd:         "/project",
		WorkingDirs: []string{"/project"},
	}
	if got, _ := ev.Skippable(in); got != SkipNone {
		t.Errorf("got %v, want SkipNone", got)
	}
}

// Empty WorkingDirs means tests that don't construct it explicitly still get cwd-only behavior.
func TestWrite_EmptyWorkingDirsFallbackToCwd(t *testing.T) {
	ev := NewWrite()
	in := Input{
		ToolName:  "Write",
		ToolInput: json.RawMessage(`{"file_path":"/project/foo.go","content":"x"}`),
		Cwd:       "/project",
	}
	if got, _ := ev.Skippable(in); got != Skip {
		t.Errorf("got %v, want Skip (cwd-only fallback)", got)
	}
}

// Write sanitizes to `${file_path}: ${content}` so the classifier sees the actual bytes about to land on disk. This is
// load-bearing: "writing 100 bytes to /tmp/x" tells the model nothing; the body is what makes a write deny-worthy.
func TestWrite_SanitizeKeepsFullContent(t *testing.T) {
	ev := NewWrite()
	body := "package main\nfunc main(){println(\"hi\")}"
	in := Input{
		ToolInput: json.RawMessage(`{"file_path":"/x.go","content":` + jsonString(body) + `}`),
	}
	out, err := ev.Sanitize(in)
	if err != nil {
		t.Fatalf("Sanitize: %v", err)
	}
	var got string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode: %v\nraw: %s", err, out)
	}
	want := "/x.go: " + body
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ─── Edit ──────────────────────────────────────────────────────────

func TestEdit_SkipsInScope(t *testing.T) {
	ev := NewEdit()
	in := Input{
		ToolName:    "Edit",
		ToolInput:   json.RawMessage(`{"file_path":"/extra/x.go","old_string":"a","new_string":"b"}`),
		Cwd:         "/project",
		WorkingDirs: []string{"/project", "/extra"},
	}
	if got, _ := ev.Skippable(in); got != Skip {
		t.Errorf("got %v, want Skip", got)
	}
}

func TestNotebookEdit_SkipsInScope(t *testing.T) {
	ev := NewNotebookEdit()
	in := Input{
		ToolName:    "NotebookEdit",
		ToolInput:   json.RawMessage(`{"notebook_path":"/project/x.ipynb","new_source":"x"}`),
		Cwd:         "/project",
		WorkingDirs: []string{"/project"},
	}
	if got, _ := ev.Skippable(in); got != Skip {
		t.Errorf("got %v, want Skip", got)
	}
}

// Edit sanitizes to `${file_path}: ${new_string}` — the replacement payload is what determines whether the edit is
// benign. old_string is dropped because the classifier reasons about what the file will look like, not what it looked
// like before.
func TestEdit_SanitizeKeepsNewString(t *testing.T) {
	ev := NewEdit()
	in := Input{
		ToolInput: json.RawMessage(`{"file_path":"/x.go","old_string":"foo","new_string":"bar","replace_all":true}`),
	}
	out, err := ev.Sanitize(in)
	if err != nil {
		t.Fatalf("Sanitize: %v", err)
	}
	var got string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode: %v\nraw: %s", err, out)
	}
	if got != "/x.go: bar" {
		t.Errorf("got %q, want %q", got, "/x.go: bar")
	}
}

// ─── NotebookEdit ──────────────────────────────────────────────────

func TestNotebookEdit_SanitizeKeepsNewSource(t *testing.T) {
	ev := NewNotebookEdit()
	src := "print('hello')"
	in := Input{
		ToolInput: json.RawMessage(`{"notebook_path":"/x.ipynb","cell_id":"c1","cell_type":"code","edit_mode":"insert","new_source":` + jsonString(src) + `}`),
	}
	out, err := ev.Sanitize(in)
	if err != nil {
		t.Fatalf("Sanitize: %v", err)
	}
	var got string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode: %v\nraw: %s", err, out)
	}
	want := "/x.ipynb insert: " + src
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNotebookEdit_DefaultModeIsReplace(t *testing.T) {
	ev := NewNotebookEdit()
	in := Input{
		ToolInput: json.RawMessage(`{"notebook_path":"/x.ipynb","new_source":"x"}`),
	}
	out, _ := ev.Sanitize(in)
	if !strings.Contains(string(out), "replace:") {
		t.Errorf("expected default edit_mode=replace, got %s", out)
	}
}
