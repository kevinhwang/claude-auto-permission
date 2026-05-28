package app

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"claude-auto-permission/internal/config"
	"claude-auto-permission/internal/decider"
	configpb "claude-auto-permission/internal/gen/config/v1"
	"claude-auto-permission/internal/hookio"
	"claude-auto-permission/internal/staticbash"
)

// testCfg is the per-package config Resolver with default rules and a writable /project/** path. Without this, the
// static decider sees no projects and every command falls through.
var testCfg = config.NewResolver(configpb.Config_builder{
	Projects: []*configpb.Project{
		configpb.Project_builder{
			PathPatterns: []string{"/**"},
			StaticBashRules: configpb.StaticBashRules_builder{
				UseDefaultRules:    configpb.UseDefaultRules_builder{}.Build(),
				AllowWritePatterns: []string{"/project/**", "/dev/**"},
			}.Build(),
		}.Build(),
	},
}.Build())

// stubDecider is a Decider that returns a fixed Result, recording each Decide call so tests can assert on call counts.
type stubDecider struct {
	name   string
	result decider.Result
	calls  int
}

func (s *stubDecider) Name() string { return s.name }
func (s *stubDecider) Decide(_ context.Context, _ *hookio.HookInput, _ decider.Env) decider.Result {
	s.calls++
	return s.result
}

// mustMarshalBashInput builds a JSON HookInput with a Bash tool_input for the PreToolUse event.
func mustMarshalBashInput(t *testing.T, command, cwd string) []byte {
	t.Helper()
	bashInput, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		t.Fatalf("marshal tool_input: %v", err)
	}
	raw, err := json.Marshal(hookio.HookInput{
		HookEventName: hookio.EventPreToolUse,
		ToolName:      "Bash",
		ToolInput:     bashInput,
		Cwd:           cwd,
	})
	if err != nil {
		t.Fatalf("marshal HookInput: %v", err)
	}
	return raw
}

func runHook(t *testing.T, raw []byte, deciders ...decider.Decider) string {
	t.Helper()
	h := &Hook{
		Config:   testCfg,
		Deciders: deciders,
	}
	var stdout bytes.Buffer
	_ = h.Run(bytes.NewReader(raw), &stdout)
	return stdout.String()
}

// TestHook_StaticAllow_EmitsProactiveAllow exercises the headline capability: PreToolUse proactively allows known-safe
// Bash, so Claude Code skips the permission prompt without bypassing user-configured permissions.deny rules.
func TestHook_StaticAllow_EmitsProactiveAllow(t *testing.T) {
	// LLM stub stays silent — only the static decider votes allow.
	llm := &stubDecider{name: "llm_classifier", result: decider.Result{Decision: decider.DecisionSilent, Reason: "test stub"}}
	out := runHook(t,
		mustMarshalBashInput(t, "git status", "/project"),
		staticbash.New(testCfg),
		llm,
	)
	if !strings.Contains(out, `"hookEventName":"PreToolUse"`) {
		t.Errorf("expected PreToolUse envelope; got %q", out)
	}
	if !strings.Contains(out, `"permissionDecision":"allow"`) {
		t.Errorf("expected proactive allow; got %q", out)
	}
}

// TestHook_LlmVetoesStaticAllow is THE load-bearing test. The static engine matches its allowlist on `git push`, but
// the LLM stub vetoes deny — the user explicitly forbade the action earlier in the session. The combiner's
// no-short-circuit-on-allow rule means the deny wins.
func TestHook_LlmVetoesStaticAllow(t *testing.T) {
	llm := &stubDecider{
		name: "llm_classifier",
		result: decider.Result{
			Decision: decider.DecisionDeny,
			Reason:   "user explicitly forbade git push",
		},
	}
	out := runHook(t,
		mustMarshalBashInput(t, "git push", "/project"),
		staticbash.New(testCfg),
		llm,
	)
	if !strings.Contains(out, `"permissionDecision":"deny"`) {
		t.Errorf("expected deny output (LLM veto over static allow); got %q", out)
	}
	if !strings.Contains(out, "user explicitly forbade git push") {
		t.Errorf("expected LLM's reason in deny message; got %q", out)
	}
	// Most important: the LLM was actually consulted despite the static engine voting allow. If short-circuit-on-allow
	// ever regresses, this counter goes to 0.
	if llm.calls != 1 {
		t.Errorf("LLM should be called once even when static allows; got %d calls", llm.calls)
	}
}

// TestHook_StaticDeny_ShortCircuits verifies the cost-only optimization: when the static decider votes deny, the LLM
// doesn't run. Today the static engine never emits Deny; this test guards against a future change forgetting to
// short-circuit.
func TestHook_StaticDeny_ShortCircuits(t *testing.T) {
	staticDeny := &stubDecider{
		name:   "static_bash_rules",
		result: decider.Result{Decision: decider.DecisionDeny, Reason: "blocked by config"},
	}
	llm := &stubDecider{
		name:   "llm_classifier",
		result: decider.Result{Decision: decider.DecisionAllow},
	}
	out := runHook(t, mustMarshalBashInput(t, "ls", "/project"), staticDeny, llm)
	if !strings.Contains(out, `"permissionDecision":"deny"`) {
		t.Errorf("expected deny; got %q", out)
	}
	if llm.calls != 0 {
		t.Errorf("LLM should not be called after a prior deny; got %d calls", llm.calls)
	}
}

// TestHook_AllSilentWritesNothing verifies the silent path — no output, Claude Code's normal flow takes over.
func TestHook_AllSilentWritesNothing(t *testing.T) {
	staticSilent := &stubDecider{name: "static_bash_rules", result: decider.Result{Decision: decider.DecisionSilent}}
	llmSilent := &stubDecider{name: "llm_classifier", result: decider.Result{Decision: decider.DecisionSilent}}
	out := runHook(t, mustMarshalBashInput(t, "unknown_command", "/project"), staticSilent, llmSilent)
	if out != "" {
		t.Errorf("expected silent (empty stdout); got %q", out)
	}
}

// TestHook_LlmAlone_AllowEmitsAllow verifies non-Bash flows: when the static decider has no Bash to evaluate (returns
// silent), the LLM's allow becomes the final allow.
func TestHook_LlmAlone_AllowEmitsAllow(t *testing.T) {
	staticSilent := &stubDecider{name: "static_bash_rules", result: decider.Result{Decision: decider.DecisionSilent}}
	llmAllow := &stubDecider{name: "llm_classifier", result: decider.Result{Decision: decider.DecisionAllow}}
	// Construct a Read input — non-Bash to keep the static path silent.
	raw, _ := json.Marshal(map[string]any{
		"hook_event_name": hookio.EventPreToolUse,
		"tool_name":       "Read",
		"tool_input":      json.RawMessage(`{"file_path":"/etc/passwd"}`),
		"cwd":             "/project",
	})
	out := runHook(t, raw, staticSilent, llmAllow)
	if !strings.Contains(out, `"permissionDecision":"allow"`) {
		t.Errorf("expected allow; got %q", out)
	}
}

// TestHook_OutputShapeMinimal pins the wire-format invariant: the allow envelope contains exactly the keys Claude
// Code's hook parser expects — no extras leak in.
func TestHook_OutputShapeMinimal(t *testing.T) {
	llm := &stubDecider{name: "llm_classifier", result: decider.Result{Decision: decider.DecisionSilent}}
	out := runHook(t, mustMarshalBashInput(t, "git status", "/project"), staticbash.New(testCfg), llm)

	var raw map[string]any
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("unmarshal: %v\nraw=%q", err, out)
	}
	if len(raw) != 1 {
		t.Errorf("expected 1 top-level key, got %v", raw)
	}
	hso, ok := raw["hookSpecificOutput"].(map[string]any)
	if !ok {
		t.Fatalf("hookSpecificOutput missing or wrong type: %v", raw)
	}
	if hso["hookEventName"] != "PreToolUse" {
		t.Errorf("hookEventName = %v, want PreToolUse", hso["hookEventName"])
	}
	if hso["permissionDecision"] != "allow" {
		t.Errorf("permissionDecision = %v, want allow", hso["permissionDecision"])
	}
}

// TestHook_AskEmitsAskOutput verifies that a DecisionAsk from any decider produces the correct "ask" wire output.
func TestHook_AskEmitsAskOutput(t *testing.T) {
	llm := &stubDecider{
		name:   "llm_classifier",
		result: decider.Result{Decision: decider.DecisionAsk, Reason: "backstop: 3 consecutive blocks"},
	}
	out := runHook(t, mustMarshalBashInput(t, "rm -rf /", "/project"), llm)
	if !strings.Contains(out, `"permissionDecision":"ask"`) {
		t.Errorf("expected ask in output, got: %s", out)
	}
	if !strings.Contains(out, "backstop") {
		t.Errorf("expected backstop reason in output, got: %s", out)
	}
}

// TestHook_StaticAllow_PlusAsk_ResolvesToAsk is THE BUG FIX test. When the static bash rules layer allows (e.g., git
// status matches its allowlist) but the LLM classifier returns ask (backstop tripped), the final output MUST be ask —
// not allow. Previously, the backstop returned silent, which let the static allow through unchecked.
func TestHook_StaticAllow_PlusAsk_ResolvesToAsk(t *testing.T) {
	staticAllow := &stubDecider{
		name:   "static_bash_rules",
		result: decider.Result{Decision: decider.DecisionAllow, Reason: "git_status_allowlist"},
	}
	backstopAsk := &stubDecider{
		name:   "llm_classifier",
		result: decider.Result{Decision: decider.DecisionAsk, Reason: "backstop: 3 consecutive blocks"},
	}
	out := runHook(t, mustMarshalBashInput(t, "git status", "/project"), staticAllow, backstopAsk)
	if !strings.Contains(out, `"permissionDecision":"ask"`) {
		t.Errorf("expected ask output (ask > allow), got: %s", out)
	}
	if strings.Contains(out, `"permissionDecision":"allow"`) {
		t.Errorf("SECURITY BUG: static allow leaked through backstop, got: %s", out)
	}
}
