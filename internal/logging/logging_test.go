package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"claude-auto-permission/internal/hookio"
)

// captureLogger returns a JSON logger writing to buf at INFO level. Tests inspect buf to assert which attrs end up on
// each line.
func captureLogger(t *testing.T) (*slog.Logger, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	return NewJSONLogger(&buf, slog.LevelInfo), &buf
}

// readLines splits a JSON log into one decoded record per line. Used instead of asserting on raw text so tests don't
// break when slog adjusts whitespace.
func readLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	out := []map[string]any{}
	for line := range strings.SplitSeq(strings.TrimRight(buf.String(), "\n"), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("bad log line %q: %v", line, err)
		}
		out = append(out, rec)
	}
	return out
}

func TestFromContext_DefaultsToPackageLogger(t *testing.T) {
	prev := Default()
	t.Cleanup(func() { SetDefault(prev) })

	l, buf := captureLogger(t)
	SetDefault(l)

	FromContext(context.Background()).Info("hello")
	lines := readLines(t, buf)
	if len(lines) != 1 || lines[0]["msg"] != "hello" {
		t.Errorf("expected one msg=hello line, got %+v", lines)
	}
}

func TestFromContext_NilContextSafe(t *testing.T) {
	prev := Default()
	t.Cleanup(func() { SetDefault(prev) })
	l, buf := captureLogger(t)
	SetDefault(l)

	// Logging with a nil ctx must not panic and must still produce a line — code paths in early init may not have a ctx
	// yet.
	var nilCtx context.Context
	FromContext(nilCtx).Info("ping")
	if buf.Len() == 0 {
		t.Error("expected output even with nil context")
	}
}

func TestWithRequest_AttachesNonEmptyFields(t *testing.T) {
	prev := Default()
	t.Cleanup(func() { SetDefault(prev) })
	l, buf := captureLogger(t)
	SetDefault(l)

	in := &hookio.HookInput{
		HookEventName:  "PreToolUse",
		SessionId:      "S1",
		Cwd:            "/proj",
		ToolName:       "Bash",
		AgentId:        "A1",
		AgentType:      "Plan",
		PermissionMode: "default",
		ToolUseId:      "tu_42",
	}
	ctx := WithRequest(context.Background(), in)
	FromContext(ctx).Info("classifier disabled", "err", "bad config")

	lines := readLines(t, buf)
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(lines))
	}
	got := lines[0]
	want := map[string]string{
		"msg":             "classifier disabled",
		"err":             "bad config",
		"session_id":      "S1",
		"hook_event":      "PreToolUse",
		"cwd":             "/proj",
		"tool":            "Bash",
		"agent_id":        "A1",
		"agent_type":      "Plan",
		"permission_mode": "default",
		"tool_use_id":     "tu_42",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("field %q = %v, want %v", k, got[k], v)
		}
	}
}

func TestWithRequest_DropsEmptyFields(t *testing.T) {
	prev := Default()
	t.Cleanup(func() { SetDefault(prev) })
	l, buf := captureLogger(t)
	SetDefault(l)

	// Parent-session, no permission_mode set — empty fields should not show up at all in the output, otherwise the JSONL
	// log gets noisy.
	in := &hookio.HookInput{
		HookEventName: "PermissionRequest",
		SessionId:     "S2",
		Cwd:           "/proj",
		ToolName:      "Read",
	}
	ctx := WithRequest(context.Background(), in)
	FromContext(ctx).Info("hi")

	lines := readLines(t, buf)
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(lines))
	}
	for _, k := range []string{"agent_id", "agent_type", "permission_mode", "tool_use_id"} {
		if _, ok := lines[0][k]; ok {
			t.Errorf("empty field %q leaked into output: %+v", k, lines[0])
		}
	}
}

func TestWithRequest_NilInputReturnsContextUnchanged(t *testing.T) {
	prev := Default()
	t.Cleanup(func() { SetDefault(prev) })
	l, _ := captureLogger(t)
	SetDefault(l)

	ctx := context.Background()
	got := WithRequest(ctx, nil)
	// We allow either ctx == got or a different ctx wrapping the same (default) logger; assert by behavior, not identity.
	if FromContext(got) != FromContext(ctx) {
		t.Error("expected nil input to leave logger unchanged")
	}
}
