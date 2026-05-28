package app

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"claude-auto-permission/internal/config"
	configpb "claude-auto-permission/internal/gen/config/v1"
	"claude-auto-permission/internal/hookio"
	"claude-auto-permission/internal/logging"
)

// TestRun_DegradationEmitsStructuredJSONLogLine drives the binary through Hook.Run with a classifier config that fails
// protovalidate (enabled=true, no provider oneof set). That trips the "classifier disabled" warn path. We assert the
// resulting line on stderr is JSON, carries the request-scoped fields the logging package binds (session_id,
// hook_event, tool, cwd), and reports the expected level/message/error.
func TestRun_DegradationEmitsStructuredJSONLogLine(t *testing.T) {
	// Redirect package-level slog to a buffer so we can inspect what the hook actually wrote. SetDefault is
	// process-global, so we restore it on cleanup.
	prev := logging.Default()
	t.Cleanup(func() { logging.SetDefault(prev) })
	var stderr bytes.Buffer
	logging.SetDefault(logging.NewJSONLogger(&stderr, slog.LevelInfo))

	// Config: classifier enabled but no provider oneof — fails proto validation in classifier.ProviderFromConfig.
	cfg := config.NewResolver(configpb.Config_builder{
		Projects: []*configpb.Project{
			configpb.Project_builder{
				PathPatterns: []string{"/**"},
				StaticBashRules: configpb.StaticBashRules_builder{
					UseDefaultRules: configpb.UseDefaultRules_builder{}.Build(),
				}.Build(),
				LlmClassifier: configpb.LlmClassifierConfig_builder{
					Enabled: ptrTrue(),
					// No provider {…} block — protovalidate rejects.
				}.Build(),
			}.Build(),
		},
	}.Build())

	h := New(cfg)
	// Drive a tool call that falls through the static engine so the classifier path is reached. Bash unknown-command falls
	// through.
	in := hookio.HookInput{
		HookEventName: hookio.EventPreToolUse,
		SessionId:     "S-degraded",
		Cwd:           "/proj",
		ToolName:      "Bash",
		ToolInput:     json.RawMessage(`{"command":"unknown-cmd"}`),
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	if err := h.Run(bytes.NewReader(raw), &bytes.Buffer{}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if stderr.Len() == 0 {
		t.Fatalf("expected a degradation log line, stderr was empty")
	}

	// Find the "classifier disabled" record. The package may emit other lines too (auto-mode policy load failures from the
	// prompt path); pick the one we care about by msg.
	var rec map[string]any
	for line := range strings.SplitSeq(strings.TrimRight(stderr.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		var got map[string]any
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("non-JSON log line %q: %v", line, err)
		}
		if got["msg"] == "classifier disabled" {
			rec = got
			break
		}
	}
	if rec == nil {
		t.Fatalf("no \"classifier disabled\" line in stderr; got:\n%s", stderr.String())
	}
	want := map[string]string{
		"session_id": "S-degraded",
		"hook_event": "PreToolUse",
		"tool":       "Bash",
		"cwd":        "/proj",
		"level":      "WARN",
	}
	for k, v := range want {
		if rec[k] != v {
			t.Errorf("field %q = %v, want %v (raw=%v)", k, rec[k], v, rec)
		}
	}
	if _, ok := rec["err"]; !ok {
		t.Errorf("expected err field on degradation log line, got %v", rec)
	}

	// Wire-format invariant: stdout stays empty for a classifier-degraded silent path. (We discard it above with
	// &bytes.Buffer{}; pin the property here too.)
	out := bytes.Buffer{}
	if err := h.Run(bytes.NewReader(raw), &out); err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "" {
		t.Errorf("expected silent stdout on degraded classifier; got %q", got)
	}
}

// ptrTrue returns *bool=true. Configpb builders take pointers for scalar fields so they can distinguish unset from
// false.
func ptrTrue() *bool {
	t := true
	return &t
}
