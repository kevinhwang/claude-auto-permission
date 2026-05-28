// Package app wires the binary's runtime dependencies into a single [Hook] value that
// `cmd/claude-auto-permission/main.go` drives.
//
// Per-invocation, [Hook.Run] resolves Claude Code's filesystem state once into a [decider.Env] and passes the same
// value to every registered decider. Per-cwd selection of classifier config happens inside the classifier, not here.
package app

import (
	"context"
	"io"

	"claude-auto-permission/internal/cachepath"
	"claude-auto-permission/internal/claudecode/claudemd"
	"claude-auto-permission/internal/claudecode/paths"
	"claude-auto-permission/internal/claudecode/permscope"
	"claude-auto-permission/internal/config"
	"claude-auto-permission/internal/decider"
	"claude-auto-permission/internal/decisionlog"
	"claude-auto-permission/internal/hookio"
	"claude-auto-permission/internal/llmclassifier"
	"claude-auto-permission/internal/logging"
	"claude-auto-permission/internal/orchestrator"
	"claude-auto-permission/internal/pathutil"
	"claude-auto-permission/internal/staticbash"
)

// Hook bundles the dependencies for one hook process invocation.
type Hook struct {
	Config       *config.Resolver
	Deciders     []decider.Decider
	Log          *decisionlog.Writer
	Instructions *claudemd.Loader
}

// New returns a Hook bound to the given config Resolver.
func New(cfg *config.Resolver) *Hook {
	runtime := cfg.Proto().GetRuntime()
	cacheDir := cachepath.Expand(runtime.GetCacheDir())

	deciders := []decider.Decider{
		staticbash.New(cfg),
		llmclassifier.New(cfg, cacheDir),
	}

	var log *decisionlog.Writer
	if anyProjectLogsDecisions(cfg) && cacheDir != "" {
		log = decisionlog.New(
			cachepath.DecisionsLog(cacheDir),
			int64(runtime.GetDecisionLogMaxBytes()),
		)
	}

	instructions := &claudemd.Loader{
		ConfigDir: pathutil.ExpandTilde(runtime.GetClaudeConfigDir()),
	}

	return &Hook{
		Config:       cfg,
		Deciders:     deciders,
		Log:          log,
		Instructions: instructions,
	}
}

// Run reads one hook event from stdin, builds the per-invocation [decider.Env], dispatches to the orchestrator, and
// writes the decision (if any) to stdout.
func (h *Hook) Run(r io.Reader, w io.Writer) error {
	input, err := hookio.Read(r)
	if err != nil {
		return nil
	}

	ccPaths := paths.Resolve(
		h.Config.Proto().GetRuntime().GetClaudeConfigDir(),
		input.Cwd,
	)
	input.ProjectRoot = ccPaths.ProjectRoot

	ctx := logging.WithRequest(context.Background(), input)
	env := h.buildEnv(ctx, input, ccPaths)
	orchestrator.Run(ctx, h.Deciders, h.Log, input, env, w)
	return nil
}

// buildEnv resolves the per-invocation environment. Each piece can fail independently; failures degrade to zero values
// rather than blocking the call.
func (h *Hook) buildEnv(ctx context.Context, in *hookio.HookInput, ccPaths paths.Paths) decider.Env {
	log := logging.FromContext(ctx)

	env := decider.Env{
		Cwd:         in.Cwd,
		ProjectRoot: ccPaths.ProjectRoot,
	}

	if scope, err := permscope.Resolve(ccPaths); err == nil {
		env.PermScope = scope
	} else {
		log.Warn("permscope resolve failed", "err", err)
	}

	if h.Instructions != nil {
		if bundle, err := h.Instructions.Load(in.Cwd); err == nil {
			env.Instructions = bundle
		} else {
			log.Warn("CLAUDE.md load failed", "err", err)
		}
	}

	return env
}

func anyProjectLogsDecisions(cfg *config.Resolver) bool {
	for _, p := range cfg.Proto().GetProjects() {
		if c := p.GetLlmClassifier(); c != nil && c.GetLogDecisions() {
			return true
		}
	}
	return false
}
