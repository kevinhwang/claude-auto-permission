// Package prompt assembles the classifier's prompt parts and owns the classifier-safe projection of the session
// transcript.
//
// Inputs to one [Build] call:
//
//   - The bundled template (`prompts.md.tmpl`, embedded): system prompt scaffold plus user-role wrap fragments.
//   - An [automodepolicy.Policy] bundle (allow / soft_deny / hard_deny / environment) sourced from
//     `claude auto-mode config`. Users customize via `autoMode.{allow, soft_deny, environment}` in the trusted Claude
//     Code settings hierarchy.
//   - Per-call state: cwd, project root, working-dir set, deny-rule patterns, resolved CLAUDE.md bundle.
//   - The sanitized transcript ([]Record) plus the proposed [CallRecord].
//
// Threat model (Defense 1 in docs/llm-classifier-design.md): the classifier sees ONLY the user's stated intent (real user prompts) and
// the agent's tool calls (sanitized inputs). [Sanitize] strips assistant prose, thinking blocks, and tool outputs
// before any [Record] reaches a [Build] call. That stripping is the linchpin of the classifier's threat model — don't
// add fields to [Record] that could carry model-authored text.
package prompt

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"text/template"

	"claude-auto-permission/internal/claudecode/automodepolicy"
	"claude-auto-permission/internal/claudecode/claudemd"
)

// memoryInstructionPrompt frames the assembled CLAUDE.md content as "user-provided codebase configuration" so the
// classifier weighs it alongside the user's stated intent. Mirrors Claude Code's `MEMORY_INSTRUCTION_PROMPT`.
const memoryInstructionPrompt = "Codebase and user instructions are shown below. Be sure to adhere to these instructions. IMPORTANT: These instructions OVERRIDE any default behavior and you MUST follow them exactly as written."

//go:embed builtin/prompts.md.tmpl
var promptTemplates string

// schemaJSON is the JSON Schema the provider passes to the model.
//
//go:embed builtin/schema.json
var schemaJSON []byte

// BuildOutput carries the assembled prompt parts. The orchestrator passes System and Schema to the provider; UserPrefix
// becomes a separate user-role API message (for repo-trusted CLAUDE.md content); User is the transcript message body.
type BuildOutput struct {
	System     string
	UserPrefix string // empty when no CLAUDE.md present
	User       string
	Schema     json.RawMessage
}

// BuildInput bundles the per-call inputs to [Build]. Every field is zero-valuable; tests pin only the fields they care
// about.
type BuildInput struct {
	Policy       automodepolicy.Policy
	Cwd          string
	ProjectRoot  string
	WorkingDirs  []string
	DenyRules    []string
	Instructions claudemd.Bundle
}

// templates parses the embedded prompt template once per process — the parse is pure and the result is read-only, so a
// one-shot init is enough.
var templates = sync.OnceValues(func() (*template.Template, error) {
	return template.New("prompts").Parse(promptTemplates)
})

// Build renders the classifier's prompt parts: system prompt, the optional CLAUDE.md user-role prefix, and the
// transcript user prompt with the proposed action appended. All framing decisions (per-section CLAUDE.md wrappers, the
// [memoryInstructionPrompt] preamble) live here because they're prompt details, not properties of any input.
func Build(in BuildInput, records []Record, proposed CallRecord) (BuildOutput, error) {
	tmpl, err := templates()
	if err != nil {
		return BuildOutput{}, fmt.Errorf("parse prompts template: %w", err)
	}

	var sysBuf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&sysBuf, "system", systemDataFor(in)); err != nil {
		return BuildOutput{}, fmt.Errorf("render system template: %w", err)
	}

	var prefixBuf bytes.Buffer
	if instr := renderInstructions(in.Instructions); instr != "" {
		if err := tmpl.ExecuteTemplate(&prefixBuf, "user_claude_md_prefix", struct{ Text string }{instr}); err != nil {
			return BuildOutput{}, fmt.Errorf("render user_claude_md_prefix: %w", err)
		}
	}

	userBody, err := buildUserBody(records, proposed)
	if err != nil {
		return BuildOutput{}, err
	}

	return BuildOutput{
		System:     sysBuf.String(),
		UserPrefix: prefixBuf.String(),
		User:       userBody,
		Schema:     json.RawMessage(schemaJSON),
	}, nil
}

// renderInstructions frames each Section as
// `Contents of <path> (project instructions, checked into the codebase):\n\n<body>` and prepends the
// [memoryInstructionPrompt] preamble. Empty bundle returns the empty string so [Build] can suppress the user-role
// prefix entirely.
func renderInstructions(bundle claudemd.Bundle) string {
	if len(bundle.Sections) == 0 {
		return ""
	}
	parts := make([]string, 0, len(bundle.Sections)+1)
	parts = append(parts, memoryInstructionPrompt)
	for _, s := range bundle.Sections {
		parts = append(parts, fmt.Sprintf(
			"Contents of %s (project instructions, checked into the codebase):\n\n%s",
			s.Path, s.Content,
		))
	}
	return strings.Join(parts, "\n\n")
}

type systemTemplateData struct {
	Allow          []string
	SoftDeny       []string
	HardDeny       []string
	Environment    []string
	Cwd            string
	ProjectRoot    string
	AdditionalDirs []string
	DenyRules      []string
}

func systemDataFor(in BuildInput) systemTemplateData {
	data := systemTemplateData{
		Allow:       in.Policy.Allow,
		SoftDeny:    in.Policy.SoftDeny,
		HardDeny:    in.Policy.HardDeny,
		Environment: in.Policy.Environment,
		Cwd:         in.Cwd,
		DenyRules:   in.DenyRules,
	}
	if in.ProjectRoot != "" && in.ProjectRoot != in.Cwd {
		data.ProjectRoot = in.ProjectRoot
	}
	for _, d := range in.WorkingDirs {
		if d == in.Cwd {
			continue
		}
		data.AdditionalDirs = append(data.AdditionalDirs, d)
	}
	return data
}

// buildUserBody serializes the sanitized transcript + proposed action as JSONL. Each line is
// `{"<ToolName>": <sanitized-input>}` or `{"user": "..."}`; the proposed action is the last line. The system prompt
// declares "the action to evaluate is the agent's most recent action," so no section headers are needed. XML provider
// modes wrap the body in `<transcript>`; forced-tool-use mode consumes the raw stream.
func buildUserBody(records []Record, proposed CallRecord) (string, error) {
	var buf bytes.Buffer
	for _, r := range records {
		blob, err := json.Marshal(r)
		if err != nil {
			return "", fmt.Errorf("marshal record: %w", err)
		}
		buf.Write(blob)
		buf.WriteByte('\n')
	}
	pBlob, err := json.Marshal(Record{Kind: KindCall, Call: &proposed})
	if err != nil {
		return "", fmt.Errorf("marshal proposed: %w", err)
	}
	buf.Write(pBlob)
	buf.WriteByte('\n')
	return buf.String(), nil
}
