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

	// Every structural section header must render, so the scaffold and all policy slots are present.
	for _, header := range []string{
		"## Environment",
		"## Classification Process",
		"## User Intent Rule",
		"## Evaluation Rules",
		"## HARD BLOCK",
		"## SOFT BLOCK",
		"## ALLOW (exceptions) if ANY of these apply",
		"## Output Format",
	} {
		if !strings.Contains(out.System, header) {
			t.Errorf("system prompt missing section %q\n--- system ---\n%s", header, out.System)
		}
	}

	// Each policy tier's rules must render under the correct section header — the template's
	// tier-routing contract, asserted by ordering so it doesn't couple to the scaffold prose.
	assertOrder(t, out.System,
		"## HARD BLOCK", "POLICY HARD 1",
		"## SOFT BLOCK", "POLICY SOFT 1",
		"## ALLOW (exceptions) if ANY of these apply", "POLICY ALLOW 1",
	)
	// Environment bullets from the policy render into the Environment section.
	assertOrder(t, out.System, "## Environment", "**Trusted repo**: github.com/me/x")

	// The XML output contract renders (rather than the forced-tool variant).
	for _, token := range []string{"<block>yes</block>", "<block>no</block>", "[Exact BLOCK Rule Name]"} {
		if !strings.Contains(out.System, token) {
			t.Errorf("system prompt missing output-contract token %q\n--- system ---\n%s", token, out.System)
		}
	}
	if strings.Contains(out.System, ForcedToolResultLine) {
		t.Errorf("system prompt should render the XML output format, got forced-tool marker")
	}

	for _, placeholder := range []string{
		"<permissions_template>",
		"<user_environment_to_replace>",
		"<settings_deny_rules>",
		"<cross_session_messages_rule>",
	} {
		if strings.Contains(out.System, placeholder) {
			t.Errorf("system prompt still contains placeholder %q\n--- system ---\n%s", placeholder, out.System)
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
	if strings.Contains(out.System, "## User Deny Rules") {
		t.Errorf("deny rules should render as a soft-block bullet, not a separate section: %s", out.System)
	}
	// The configured deny patterns must be interpolated into the rendered block.
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
	// The resolved CLAUDE.md must be wrapped in the delimiter tags with its path header, so the model can
	// tell repo-provided instructions from the classifier's own prompt.
	for _, want := range []string{
		"<user_claude_md>",
		"Do the thing.",
		"</user_claude_md>",
		"Contents of /proj/CLAUDE.md (project instructions, checked into the codebase):",
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

func TestBuild_SessionContextUsesSanitizedIdentityForUserBranchRules(t *testing.T) {
	t.Setenv("GITHUB_ACTOR", "alice/acme@example.com")
	t.Setenv("USER", "ignored")
	t.Setenv("USERNAME", "ignored")

	out, err := Build(BuildInput{
		Policy:    samplePolicy(),
		DenyRules: []string{"Bash(git push origin $USER/*)"},
	}, nil, CallRecord{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.Contains(out.System, "## Session Context") {
		t.Fatalf("missing Session Context block: %s", out.System)
	}
	if !strings.Contains(out.System, "`aliceacmeexample.com`") {
		t.Errorf("identity was not sanitized as expected: %s", out.System)
	}
	if !strings.Contains(out.System, "$USER/...") {
		t.Errorf("missing branch-pattern explanation: %s", out.System)
	}
}

func TestBuild_SessionContextOmittedWithoutUserBranchRules(t *testing.T) {
	t.Setenv("GITHUB_ACTOR", "alice")

	out, err := Build(BuildInput{
		Policy:    samplePolicy(),
		DenyRules: []string{"Bash(git push origin main)"},
	}, nil, CallRecord{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if strings.Contains(out.System, "## Session Context") {
		t.Errorf("Session Context should render only for $USER branch rules: %s", out.System)
	}
}

// assertOrder checks that each substring appears in s strictly after the previous one. Used to assert
// structural routing (a policy rule renders under its tier's header) without pinning the surrounding prose.
func assertOrder(t *testing.T, s string, subs ...string) {
	t.Helper()
	from := 0
	for i, sub := range subs {
		idx := strings.Index(s[from:], sub)
		if idx < 0 {
			t.Errorf("expected %q at/after position %d (following %v); not found\n--- text ---\n%s", sub, from, subs[:i], s)
			return
		}
		from += idx + len(sub)
	}
}

func TestSanitizeUserIdentity(t *testing.T) {
	got := sanitizeUserIdentity("abc/DEF@example.com_with-extra-and-a-very-long-tail-that-should-be-clipped")
	if strings.ContainsAny(got, "/@") {
		t.Errorf("identity contains disallowed chars: %q", got)
	}
	if len(got) > 64 {
		t.Errorf("identity length = %d, want <= 64", len(got))
	}
}
