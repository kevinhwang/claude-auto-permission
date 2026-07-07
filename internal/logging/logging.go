// Package logging provides one *slog.Logger per hook invocation attached to context.Context (via WithRequest).
// FromContext(ctx) returns it pre-bound with session_id / cwd / hook_event / tool, so downstream callsites don't
// re-derive them.
//
// Concurrent Claude Code sessions interleave their hook output; the per-request fields are what make the JSONL log
// readable.
//
// Default: JSON handler at INFO writing to os.Stderr. SetDefault is a test seam.
package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync"

	"claude-auto-permission/internal/hookio"
)

// ctxKey is unexported so callers can't pull the logger out by key and bypass FromContext.
type ctxKey struct{}

var (
	defaultMu sync.RWMutex
	// defaultLogger is the fallback when no per-request logger is on the context. Lazy so SetDefault can override before
	// first use.
	defaultLogger *slog.Logger
)

// Default returns the package-level fallback. Prefer FromContext(ctx).
func Default() *slog.Logger {
	defaultMu.RLock()
	l := defaultLogger
	defaultMu.RUnlock()
	if l != nil {
		return l
	}
	defaultMu.Lock()
	defer defaultMu.Unlock()
	if defaultLogger == nil {
		defaultLogger = newJSONLogger(os.Stderr, slog.LevelInfo)
	}
	return defaultLogger
}

// SetDefault installs the fallback logger. Test seam; production code shouldn't call it after init.
func SetDefault(l *slog.Logger) {
	defaultMu.Lock()
	defer defaultMu.Unlock()
	defaultLogger = l
}

// NewJSONLogger builds a JSON slog.Logger writing to w at level.
func NewJSONLogger(w io.Writer, level slog.Level) *slog.Logger {
	return newJSONLogger(w, level)
}

func newJSONLogger(w io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))
}

// WithRequest attaches a per-request logger pre-bound with the request's identifying fields (session_id, hook_event,
// tool, …).
//
// Empty values are dropped to avoid cluttering the log with fields that don't apply to the current event (e.g. agent_id
// on a parent-session call). nil input returns ctx unchanged — used by the early stdin-decode path before we know the
// request.
func WithRequest(ctx context.Context, in *hookio.HookInput) context.Context {
	base := FromContext(ctx)
	if in == nil {
		return ctx
	}
	attrs := make([]any, 0, 16)
	addAttr := func(key, val string) {
		if val != "" {
			attrs = append(attrs, key, val)
		}
	}
	addAttr("session_id", in.SessionId)
	addAttr("hook_event", in.HookEventName)
	addAttr("tool", in.ToolName)
	addAttr("cwd", in.Cwd)
	addAttr("agent_id", in.AgentId)
	addAttr("agent_type", in.AgentType)
	addAttr("permission_mode", in.PermissionMode)
	addAttr("tool_use_id", in.ToolUseId)
	if len(attrs) == 0 {
		return WithLogger(ctx, base)
	}
	return WithLogger(ctx, base.With(attrs...))
}

// WithLogger stores l on ctx. Use with FromContext(ctx).With(...) to attach extra structured fields beyond the
// per-request defaults.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	if l == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, l)
}

// FromContext returns the logger attached to ctx, or the package default. Always non-nil — callers don't need to
// nil-check.
func FromContext(ctx context.Context) *slog.Logger {
	if ctx == nil {
		return Default()
	}
	if l, ok := ctx.Value(ctxKey{}).(*slog.Logger); ok && l != nil {
		return l
	}
	return Default()
}
