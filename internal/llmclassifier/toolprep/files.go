package toolprep

import (
	"encoding/json"

	"claude-auto-permission/internal/pathutil"
)

// Plugins for file ops (Read / Write / Edit / NotebookEdit). Each skips the classifier when the target path is inside
// the user's working-dir set (cwd ∪ `permissions.additionalDirectories`) and lets Claude Code's normal flow handle the
// call; out-of-scope paths (~/.aws, /etc, …) reach the classifier.
//
// Sanitization keeps bodies verbatim — the classifier needs to see what's about to be written. Volume is handled at the
// transcript level (provider "prompt too long" maps to unavailability), not by truncating the proposed action.

// inWorkingDirs reports whether the path at `pathField` resolves under any configured working dir. Decode failure or
// empty path returns false so the classifier still weighs in.
func inWorkingDirs(in Input, pathField string) bool {
	var raw map[string]any
	if err := json.Unmarshal(in.ToolInput, &raw); err != nil {
		return false
	}
	path, _ := raw[pathField].(string)
	if path == "" {
		return false
	}
	return isUnderAny(path, workingDirs(in))
}

// workingDirs returns the orchestrator's resolved set, falling back to a cwd-only set when empty (so hand-built test
// Inputs still work).
func workingDirs(in Input) []string {
	if len(in.WorkingDirs) > 0 {
		return in.WorkingDirs
	}
	if in.Cwd != "" {
		return []string{in.Cwd}
	}
	return nil
}

func isUnderAny(path string, dirs []string) bool {
	if path == "" || len(dirs) == 0 {
		return false
	}
	for _, d := range dirs {
		if pathutil.IsPathUnder(path, d) {
			return true
		}
	}
	return false
}

// Read skips when the path is inside the working-dir set. Out-of-scope reads (~/.aws, /etc, …) reach the classifier.
type Read struct{}

func NewRead() Read { return Read{} }

func (Read) Skippable(in Input) (Skippable, string) {
	if inWorkingDirs(in, "file_path") {
		return Skip, "skipped: in-cwd Read"
	}
	return SkipNone, ""
}

func (Read) Sanitize(in Input) (json.RawMessage, error) {
	var input struct {
		FilePath string `json:"file_path"`
	}
	_ = json.Unmarshal(in.ToolInput, &input)
	return json.Marshal(input.FilePath)
}

// Write skips when in working-dir scope (Claude Code's own session state may already authorize via
// additionalDirectories we can't see). Sanitize emits `{file_path}: {content}`.
type Write struct{}

func NewWrite() Write { return Write{} }

func (Write) Skippable(in Input) (Skippable, string) {
	if inWorkingDirs(in, "file_path") {
		return Skip, "skipped: in-cwd Write"
	}
	return SkipNone, ""
}

func (Write) Sanitize(in Input) (json.RawMessage, error) {
	var input struct {
		FilePath string `json:"file_path"`
		Content  string `json:"content"`
	}
	_ = json.Unmarshal(in.ToolInput, &input)
	return json.Marshal(input.FilePath + ": " + input.Content)
}

// Edit follows Write's skip rule. Sanitize emits `{file_path}: {new_string}`.
type Edit struct{}

func NewEdit() Edit { return Edit{} }

func (Edit) Skippable(in Input) (Skippable, string) {
	if inWorkingDirs(in, "file_path") {
		return Skip, "skipped: in-cwd Edit"
	}
	return SkipNone, ""
}

func (Edit) Sanitize(in Input) (json.RawMessage, error) {
	var input struct {
		FilePath  string `json:"file_path"`
		NewString string `json:"new_string"`
	}
	_ = json.Unmarshal(in.ToolInput, &input)
	return json.Marshal(input.FilePath + ": " + input.NewString)
}

// NotebookEdit follows Edit's skip rule. Sanitize emits `{notebook_path} {edit_mode}: {new_source}`.
type NotebookEdit struct{}

func NewNotebookEdit() NotebookEdit { return NotebookEdit{} }

func (NotebookEdit) Skippable(in Input) (Skippable, string) {
	if inWorkingDirs(in, "notebook_path") {
		return Skip, "skipped: in-cwd NotebookEdit"
	}
	return SkipNone, ""
}

func (NotebookEdit) Sanitize(in Input) (json.RawMessage, error) {
	var input struct {
		NotebookPath string `json:"notebook_path"`
		EditMode     string `json:"edit_mode"`
		NewSource    string `json:"new_source"`
	}
	_ = json.Unmarshal(in.ToolInput, &input)
	mode := input.EditMode
	if mode == "" {
		mode = "replace"
	}
	return json.Marshal(input.NotebookPath + " " + mode + ": " + input.NewSource)
}
