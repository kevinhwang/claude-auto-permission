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
	"os"
	"os/exec"
	"strings"
	"sync"
	"text/template"

	"claude-auto-permission/internal/claudecode/automodepolicy"
	"claude-auto-permission/internal/claudecode/claudemd"
)

// XMLOutputFormat is the reference classifier output contract. XML provider modes use it directly; the legacy
// forced-tool provider mode rewrites it to [ForcedToolResultLine] before sending the request.
const XMLOutputFormat = "## Output Format\n\n" +
	"If the action should be blocked:\n" +
	"<block>yes</block><reason>[Exact BLOCK Rule Name] one short sentence</reason>\n\n" +
	"If the action should be allowed:\n" +
	"<block>no</block>\n\n" +
	"The reason MUST begin with the exact name of the matching BLOCK rule in square brackets (e.g. `[Irreversible Local Destruction]`); for a user-boundary or undecodable-command block, name the territory rule it touches instead. If you cannot name a specific BLOCK rule, then nothing matched — output `<block>no</block>`.\n" +
	"Do NOT include a <reason> tag when the action is allowed.\n" +
	"Your ENTIRE response MUST begin with <block>. Do NOT emit any analysis, reasoning, or commentary before <block> — no \"Looking at...\" or similar preamble."

// ForcedToolResultLine is the structured-output instruction used by the legacy forced-tool provider mode.
const ForcedToolResultLine = "Use the classify_result tool to report your classification."

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
// transcript user prompt with the proposed action appended. Prose framing lives in the template; this assembles the
// per-section CLAUDE.md bodies the `user_claude_md_prefix` wrapper renders.
func Build(in BuildInput, records []Record, proposed CallRecord) (BuildOutput, error) {
	tmpl, err := templates()
	if err != nil {
		return BuildOutput{}, fmt.Errorf("parse prompts template: %w", err)
	}

	var sysBuf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&sysBuf, "system", systemDataFor(in)); err != nil {
		return BuildOutput{}, fmt.Errorf("render system template: %w", err)
	}
	system := sysBuf.String()
	if ctx := sessionContextBlock(in); ctx != "" {
		system = strings.TrimRight(system, "\n") + "\n\n" + ctx
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
		System:     system,
		UserPrefix: prefixBuf.String(),
		User:       userBody,
		Schema:     json.RawMessage(schemaJSON),
	}, nil
}

func sessionContextBlock(in BuildInput) string {
	if !needsSessionUserContext(in) {
		return ""
	}
	user := sessionUserIdentity()
	if user == "" {
		return ""
	}
	return fmt.Sprintf(
		"## Session Context\n\n- **User identity**: `%s`. The `$USER/...` pattern in the rules above resolves to `%s/...`. Branches whose first path segment is a different person's name (`<other-user>/...`) are NOT this user's personal branches.",
		user,
		user,
	)
}

func needsSessionUserContext(in BuildInput) bool {
	for _, rules := range [][]string{in.Policy.Allow, in.Policy.SoftDeny, in.Policy.HardDeny, in.DenyRules} {
		for _, rule := range rules {
			if strings.Contains(rule, "$USER/") {
				return true
			}
		}
	}
	return false
}

func sessionUserIdentity() string {
	for _, key := range []string{"GITHUB_ACTOR", "USER", "USERNAME"} {
		if id := sanitizeUserIdentity(os.Getenv(key)); id != "" {
			return id
		}
	}
	out, err := exec.Command("git", "config", "--get", "user.email").Output()
	if err != nil {
		return ""
	}
	email := strings.TrimSpace(string(out))
	if before, _, ok := strings.Cut(email, "@"); ok {
		email = before
	}
	return sanitizeUserIdentity(email)
}

func sanitizeUserIdentity(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.', r == '_', r == '-':
			b.WriteRune(r)
		}
		if b.Len() >= 64 {
			break
		}
	}
	return b.String()
}

// renderInstructions frames each Section as
// `Contents of <path> (project instructions, checked into the codebase):\n\n<body>`, joined for the
// `user_claude_md_prefix` wrapper to embed. Empty bundle returns the empty string so [Build] can suppress the
// user-role prefix entirely.
func renderInstructions(bundle claudemd.Bundle) string {
	if len(bundle.Sections) == 0 {
		return ""
	}
	parts := make([]string, 0, len(bundle.Sections))
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
	OutputFormat   string
}

func systemDataFor(in BuildInput) systemTemplateData {
	// The system prompt must never render an empty Environment section (e.g. when policy loading fell back with no
	// environment, or the `claude` binary was missing on a fresh install). Backfill from the bundled baseline so the
	// fallback lives in exactly one place rather than being duplicated in the template.
	environment := in.Policy.Environment
	if len(environment) == 0 {
		environment = automodepolicy.BundledDefaults().Environment
	}
	data := systemTemplateData{
		Allow:        in.Policy.Allow,
		SoftDeny:     in.Policy.SoftDeny,
		HardDeny:     in.Policy.HardDeny,
		Environment:  environment,
		Cwd:          in.Cwd,
		DenyRules:    in.DenyRules,
		OutputFormat: XMLOutputFormat,
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
