package prompt

import (
	"encoding/json"
	"strings"
	"testing"

	"claude-auto-permission/internal/claudecode/automodepolicy"
	"claude-auto-permission/internal/claudecode/claudemd"
)

func samplePolicy() automodepolicy.Policy {
	return automodepolicy.Policy{
		Allow:    []string{"POLICY ALLOW 1"},
		SoftDeny: []string{"POLICY SOFT 1"},
		HardDeny: []string{"POLICY HARD 1"},
		Environment: []string{
			"**Trusted repo**: github.com/me/x",
		},
	}
}

func TestBuild_RendersAllSections(t *testing.T) {
	out, err := Build(BuildInput{Policy: samplePolicy()}, nil, CallRecord{Tool: "Bash"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Section headers from Claude Code's reference scaffold.
	for _, want := range []string{
		"## Environment",
		"**Trusted repo**: github.com/me/x",
		"## ALLOW (exceptions) if ANY of these apply",
		"POLICY ALLOW 1",
		"## SOFT BLOCK",
		"POLICY SOFT 1",
		"## HARD BLOCK",
		"POLICY HARD 1",
		"## Classification Process",
		"## User Intent Rule",
		"## Evaluation Rules",
		"Use the classify_result tool",
	} {
		if !strings.Contains(out.System, want) {
			t.Errorf("system prompt missing %q\n--- system ---\n%s", want, out.System)
		}
	}
	// User prompt is a flat JSONL stream — no section headers.
	if strings.Contains(out.User, "## Session") || strings.Contains(out.User, "## Proposed action") {
		t.Errorf("user prompt should not contain section headers: %s", out.User)
	}
	if !strings.Contains(out.User, `"Bash":`) {
		t.Errorf("proposed action JSON missing tool key: %s", out.User)
	}
	if out.UserPrefix != "" {
		t.Errorf("UserPrefix should be empty when no instructions set; got %q", out.UserPrefix)
	}
	if !strings.Contains(string(out.Schema), "shouldBlock") {
		t.Errorf("schema missing shouldBlock: %s", out.Schema)
	}
	var schemaObj map[string]any
	if err := json.Unmarshal(out.Schema, &schemaObj); err != nil {
		t.Errorf("schema not valid JSON: %v", err)
	}
}

// The system prompt is identical across calls with the same input. Pinning this prevents an accidental introduction of
// per-call nondeterminism.
func TestBuild_SystemPromptStable(t *testing.T) {
	in := BuildInput{Policy: samplePolicy()}
	a, _ := Build(in, nil, CallRecord{})
	b, _ := Build(in, nil, CallRecord{})
	if a.System != b.System {
		t.Errorf("system prompt not stable across calls\n--- a ---\n%s\n--- b ---\n%s", a.System, b.System)
	}
}

func TestBuild_PolicyEnvironmentRendered(t *testing.T) {
	out, _ := Build(BuildInput{Policy: samplePolicy()}, nil, CallRecord{})
	if !strings.Contains(out.System, "**Trusted repo**: github.com/me/x") {
		t.Errorf("expected policy environment to render: %s", out.System)
	}
}

func TestBuild_EmptyPolicyRendersFallbackEnvironment(t *testing.T) {
	out, _ := Build(BuildInput{}, nil, CallRecord{})
	// An empty policy backfills its Environment from the bundled baseline, so every bundled environment line must
	// render. Asserting against BundledDefaults directly keeps this test from drifting when the baseline is reworded.
	for _, want := range automodepolicy.BundledDefaults().Environment {
		if !strings.Contains(out.System, want) {
			t.Errorf("fallback env missing %q: %s", want, out.System)
		}
	}
}

func TestBuild_TranscriptRecordsSerialized(t *testing.T) {
	records := []Record{
		{Kind: KindUser, User: "fix the build"},
		{Kind: KindCall, Call: &CallRecord{Tool: "Read", ToolInput: json.RawMessage(`"/x"`)}},
	}
	out, err := Build(
		BuildInput{Policy: samplePolicy()},
		records,
		CallRecord{Tool: "Bash", ToolInput: json.RawMessage(`"ls"`)},
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, want := range []string{
		`{"user":"fix the build"}`,
		`{"Read":"/x"}`,
		`{"Bash":"ls"}`,
	} {
		if !strings.Contains(out.User, want) {
			t.Errorf("user prompt missing %q\n--- user ---\n%s", want, out.User)
		}
	}
	if strings.Contains(out.User, "_result") {
		t.Errorf("user prompt must not contain `_result` lines: %s", out.User)
	}
}

func TestBuild_EmptyPolicy(t *testing.T) {
	out, err := Build(BuildInput{}, nil, CallRecord{Tool: "Bash"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, want := range []string{"## Environment", "## ALLOW", "## SOFT BLOCK", "## HARD BLOCK"} {
		if !strings.Contains(out.System, want) {
			t.Errorf("system prompt missing %q", want)
		}
	}
	if out.User == "" || len(out.Schema) == 0 {
		t.Error("user prompt or schema empty")
	}
}

func TestBuild_WorkingDirsRenderedInEnvironment(t *testing.T) {
	out, _ := Build(BuildInput{
		Policy:      samplePolicy(),
		Cwd:         "/proj",
		WorkingDirs: []string{"/proj", "/extra-1", "/extra-2"},
	}, nil, CallRecord{})

	if !strings.Contains(out.System, "**Working directories**:") {
		t.Errorf("missing Working directories bullet: %s", out.System)
	}
	if !strings.Contains(out.System, "cwd: `/proj`") {
		t.Errorf("missing cwd entry: %s", out.System)
	}
	if !strings.Contains(out.System, "/extra-1") || !strings.Contains(out.System, "/extra-2") {
		t.Errorf("missing additional dirs: %s", out.System)
	}
	// cwd should not appear twice (once as cwd, once in additional list).
	if strings.Count(out.System, "/proj") > 2 {
		t.Errorf("/proj appears %d times — likely duplicated", strings.Count(out.System, "/proj"))
	}
}

func TestBuild_DenyRulesRenderedInSettingsBlock(t *testing.T) {
	out, _ := Build(BuildInput{
		Policy:    samplePolicy(),
		DenyRules: []string{"Bash(rm:*)", "Edit(/etc/**)"},
	}, nil, CallRecord{})
	if !strings.Contains(out.System, "User Deny Rules") {
		t.Errorf("missing User Deny Rules block: %s", out.System)
	}
	for _, want := range []string{"Bash(rm:*)", "Edit(/etc/**)"} {
		if !strings.Contains(out.System, want) {
			t.Errorf("missing rule %q: %s", want, out.System)
		}
	}
}

func TestBuild_NoDenyRulesOmitsBlock(t *testing.T) {
	out, _ := Build(BuildInput{Policy: samplePolicy()}, nil, CallRecord{})
	if strings.Contains(out.System, "User Deny Rules") {
		t.Errorf("User Deny Rules block should be empty when no rules: %s", out.System)
	}
}

func TestBuild_InstructionsRenderedAsUserPrefix(t *testing.T) {
	out, _ := Build(BuildInput{
		Policy: samplePolicy(),
		Instructions: claudemd.Bundle{
			Sections: []claudemd.Section{
				{Path: "/proj/CLAUDE.md", Content: "Do the thing."},
			},
		},
	}, nil, CallRecord{})

	if out.UserPrefix == "" {
		t.Fatal("UserPrefix is empty; expected CLAUDE.md content wrapped")
	}
	for _, want := range []string{
		"<user_claude_md>",
		"Do the thing.",
		"</user_claude_md>",
		"Codebase and user instructions are shown below.",
		"Contents of /proj/CLAUDE.md (project instructions, checked into the codebase):",
		// The wrap framing matches the reference impl (yoloClassifier.ts:469-471) — don't soften it without re-auditing.
		"part of the user's intent",
	} {
		if !strings.Contains(out.UserPrefix, want) {
			t.Errorf("UserPrefix missing %q\n--- prefix ---\n%s", want, out.UserPrefix)
		}
	}
	// Instructions must NOT leak into the system prompt — they should be a separate user-role message channel.
	if strings.Contains(out.System, "# CLAUDE.md") {
		t.Errorf("CLAUDE.md content leaked into system prompt: %s", out.System)
	}
}

func TestBuild_NoInstructionsLeavesPrefixEmpty(t *testing.T) {
	out, _ := Build(BuildInput{Policy: samplePolicy()}, nil, CallRecord{})
	if out.UserPrefix != "" {
		t.Errorf("UserPrefix should be empty when no instructions; got %q", out.UserPrefix)
	}
}
