package llmclassifier

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	cctranscript "claude-auto-permission/internal/claudecode/transcript"
	"claude-auto-permission/internal/decider"
	configpb "claude-auto-permission/internal/gen/config/v1"
	"claude-auto-permission/internal/hookio"
	"claude-auto-permission/internal/llmclassifier/prompt"
	"claude-auto-permission/internal/llmclassifier/providers"
	"claude-auto-permission/internal/llmclassifier/state"
	"claude-auto-permission/internal/llmclassifier/toolprep"
	"claude-auto-permission/internal/logging"
)

// step carries the per-call mutable state through the pipeline. Each phase reads what's already there and (optionally)
// populates fields later phases consume. `start` is captured up-front so latency measurements span the whole pipeline.
type step struct {
	start time.Time
	in    *hookio.HookInput
	env   decider.Env

	// Set by matchProjectConfig.
	pcfg     *configpb.LlmClassifierConfig
	classCfg Config

	// Set by sanitizeProposedAction.
	proposed prompt.CallRecord

	// Set by loadTranscript.
	records []prompt.Record

	// Set by buildPrompt.
	prompt prompt.BuildOutput

	// Set by resolveProvider.
	provider providers.Provider
}

// runPipeline is the spine of [Decider.Decide]. Each phase returns `(result, stop)`; the spine threads them and bails
// on the first stop. Phases are listed in the order the package-level doc lists them.
func (d *Decider) runPipeline(ctx context.Context, in *hookio.HookInput, env decider.Env) decider.Result {
	s := &step{start: time.Now(), in: in, env: env}

	for _, phase := range []func(context.Context, *step) (decider.Result, bool){
		d.checkInput,
		d.matchProjectConfig,
		d.checkBackstop,
		d.checkPermissionMode,
		d.checkSkippable,
		d.sanitizeProposedAction,
		d.loadTranscriptPhase,
		d.buildPromptPhase,
		d.resolveProvider,
	} {
		if r, stop := phase(ctx, s); stop {
			return r
		}
	}
	return d.classifyAndRecord(ctx, s)
}

// checkInput rejects nil or empty inputs.
func (d *Decider) checkInput(_ context.Context, s *step) (decider.Result, bool) {
	if s.in == nil || s.in.ToolName == "" {
		return d.silent("nil or empty hook input", s.start), true
	}
	return decider.Result{}, false
}

// matchProjectConfig selects the per-project classifier config block and stops if the classifier is disabled for this
// project.
func (d *Decider) matchProjectConfig(_ context.Context, s *step) (decider.Result, bool) {
	s.pcfg = d.cfg.MatchingLlmClassifier(s.env.ProjectRoot)
	if s.pcfg == nil || !s.pcfg.GetEnabled() {
		return d.silent("classifier disabled for this project", s.start), true
	}
	s.classCfg = ConfigFromProto(s.pcfg)
	return decider.Result{}, false
}

// checkBackstop honors the per-session block budget. When tripped: reset the counters (one-shot ask) and return Ask
// unless the TTL has expired, in which case fall through and classify fresh.
func (d *Decider) checkBackstop(_ context.Context, s *step) (decider.Result, bool) {
	if s.in.SessionId == "" {
		return decider.Result{}, false
	}
	snap, err := d.stateStore.Get(s.in.SessionId)
	if err != nil || !snap.Disabled() {
		return decider.Result{}, false
	}
	_, _ = d.stateStore.Reset(s.in.SessionId)
	if snap.Expired(s.classCfg.BackstopTtl) {
		return decider.Result{}, false
	}
	return d.ask("backstop: "+snap.AutoDisableReason, s.start), true
}

// checkPermissionMode honors `bypassPermissions` (silent — user opted out of approvals) and `acceptEdits` (silent for
// file-mutating tools — blanket edit consent).
func (d *Decider) checkPermissionMode(_ context.Context, s *step) (decider.Result, bool) {
	switch s.in.PermissionMode {
	case "bypassPermissions":
		return d.silent("permission_mode=bypassPermissions", s.start), true
	case "acceptEdits":
		if isFileMutatingTool(s.in.ToolName) {
			return d.silent("permission_mode=acceptEdits + file-mutating tool", s.start), true
		}
	}
	return decider.Result{}, false
}

// checkSkippable consults the per-tool evaluator's skip verdict. On Skip, vote silent so Claude Code's normal flow
// handles the call.
func (d *Decider) checkSkippable(_ context.Context, s *step) (decider.Result, bool) {
	ev := d.tools.Tool(s.in.ToolName)
	skip, reason := ev.Skippable(d.evalInput(s))
	if skip == toolprep.Skip {
		return d.silent(reason, s.start), true
	}
	return decider.Result{}, false
}

// sanitizeProposedAction projects the raw tool input down to a classifier-safe [prompt.CallRecord]. A buggy sanitizer
// must not poison the whole flow — [safeSanitize] catches panics and falls back to the raw input.
func (d *Decider) sanitizeProposedAction(_ context.Context, s *step) (decider.Result, bool) {
	ev := d.tools.Tool(s.in.ToolName)
	s.proposed = prompt.CallRecord{
		Tool:      s.in.ToolName,
		ToolInput: safeSanitize(ev, d.evalInput(s)),
	}
	return decider.Result{}, false
}

// loadTranscriptPhase reads + sanitizes parent and (when applicable) subagent transcripts.
func (d *Decider) loadTranscriptPhase(_ context.Context, s *step) (decider.Result, bool) {
	records, err := d.loadTranscript(s.in)
	if err != nil {
		return d.onError("transcript read error: "+err.Error(), s.start, s.classCfg.OnClassifierError), true
	}
	s.records = records
	return decider.Result{}, false
}

// buildPromptPhase loads the auto-mode policy and renders the prompt parts.
func (d *Decider) buildPromptPhase(ctx context.Context, s *step) (decider.Result, bool) {
	po, err := d.buildPrompt(ctx, s.env, s.classCfg, s.records, s.proposed)
	if err != nil {
		return d.onError("build prompt: "+err.Error(), s.start, s.classCfg.OnClassifierError), true
	}
	s.prompt = po
	return decider.Result{}, false
}

// resolveProvider builds (or fetches the cached) provider for this project. Construction errors are infrastructure
// failures: the `on_classifier_error` knob picks between passthrough (default — Claude Code's normal flow handles it)
// and ask (forced prompt).
func (d *Decider) resolveProvider(ctx context.Context, s *step) (decider.Result, bool) {
	p, err := d.providerFor(ctx, s.env.ProjectRoot, s.pcfg)
	if err != nil {
		logging.FromContext(ctx).Warn("classifier disabled", "err", err)
		return d.onError("provider construction: "+err.Error(), s.start, s.classCfg.OnClassifierError), true
	}
	s.provider = p
	return decider.Result{}, false
}

// classifyAndRecord makes the LLM call, dumps the round-trip when enabled, persists the per-session counters, and emits
// the final vote. This is the only phase that runs unconditionally — the pipeline either gets here or returned earlier.
func (d *Decider) classifyAndRecord(ctx context.Context, s *step) decider.Result {
	provCtx := ctx
	if s.classCfg.Timeout > 0 {
		var cancel context.CancelFunc
		provCtx, cancel = context.WithTimeout(ctx, s.classCfg.Timeout)
		defer cancel()
	}
	req := providers.Request{
		SystemPrompt: s.prompt.System,
		UserPrefix:   s.prompt.UserPrefix,
		UserPrompt:   s.prompt.User,
		Schema:       s.prompt.Schema,
	}
	res, perr := s.provider.Classify(provCtx, req)
	maybeDump(d.dumpDir, s.provider.Name(), req, res, perr)
	if perr != nil {
		// Infrastructure failure. Don't bump the block counter — the backstop is for runaway blocks, not outages — and route
		// via the per-project knob (Passthrough by default, Ask when set).
		out := d.onError(fmt.Sprintf("%s: %s", s.provider.Name(), perr.Error()), s.start, s.classCfg.OnClassifierError)
		out.Meta = meta(s.provider)
		return out
	}

	if s.in.SessionId != "" {
		if res.ShouldBlock {
			_, _ = d.stateStore.Block(s.in.SessionId, res.Reason, state.Limits{
				MaxConsecutive: s.classCfg.MaxConsecutiveBlocks,
				MaxTotal:       s.classCfg.MaxSessionBlocks,
			})
		} else {
			_, _ = d.stateStore.Allow(s.in.SessionId)
		}
	}

	out := d.verdictVote(res, s.classCfg.Mode)
	out.LatencyMs = ms(s.start)
	out.Meta = meta(s.provider)
	return out
}

// verdictVote maps a provider verdict to a decider vote. A block is always a deny. A no-block verdict is an allow in
// FullAuto, but in BlockOnly it becomes silent — the classifier withholds approval authority, deferring the call to
// peer deciders (e.g. the static Bash allowlist) and Claude Code's normal flow. The block path is identical in both
// modes, so the transcript-aware veto (the headline benefit) is unaffected.
func (d *Decider) verdictVote(res providers.Result, mode Mode) decider.Result {
	if res.ShouldBlock {
		return decider.Deny(res.Reason)
	}
	if mode == ModeBlockOnly {
		return decider.Silent("block_only mode: no-block verdict emitted as no opinion")
	}
	return decider.Allow(res.Reason)
}

// evalInput builds the [toolprep.Input] for the per-tool evaluator. Captured here so [checkSkippable] and
// [sanitizeProposedAction] don't drift apart.
func (d *Decider) evalInput(s *step) toolprep.Input {
	return toolprep.Input{
		ToolName:    s.in.ToolName,
		ToolInput:   s.in.ToolInput,
		Cwd:         s.env.Cwd,
		ProjectRoot: s.env.ProjectRoot,
		WorkingDirs: s.env.PermScope.WorkingDirs,
	}
}

// loadTranscript reads parent + (if any) subagent transcripts and sanitizes them. A missing transcript is fine — fresh
// session, fixture, or odd Claude Code config — and returns an empty list.
func (d *Decider) loadTranscript(in *hookio.HookInput) ([]prompt.Record, error) {
	path := cctranscript.LocatePath(in.TranscriptPath)
	if path == "" {
		return nil, nil
	}

	var sources []cctranscript.Source
	if fileExists(path) {
		sources = append(sources, cctranscript.Source{Path: path})
	}
	if in.AgentId != "" {
		subPath := cctranscript.SubagentTranscriptPath(path, in.AgentId)
		if fileExists(subPath) {
			sources = append(sources, cctranscript.Source{
				Path:         subPath,
				SubagentType: in.AgentType,
			})
		}
	}
	if len(sources) == 0 {
		return nil, nil
	}

	entries, err := cctranscript.ReadAll(sources...)
	if err != nil {
		return nil, fmt.Errorf("read transcript: %w", err)
	}

	sanitizer := func(toolName string, raw json.RawMessage) (json.RawMessage, error) {
		ev := d.tools.Tool(toolName)
		return safeSanitize(ev, toolprep.Input{
			ToolName:  toolName,
			ToolInput: raw,
		}), nil
	}

	return prompt.Sanitize(entries, sanitizer)
}

// safeSanitize invokes ev.Sanitize, falling back to the raw input on error or panic. Tool inputs in transcript history
// are unvalidated model output; a buggy sanitizer must not poison the whole flow.
func safeSanitize(ev toolprep.Tool, in toolprep.Input) (out json.RawMessage) {
	defer func() {
		if r := recover(); r != nil {
			out = fallbackInput(in.ToolInput)
		}
	}()
	got, err := ev.Sanitize(in)
	if err != nil {
		return fallbackInput(in.ToolInput)
	}
	return got
}

// fallbackInput returns raw if it's valid JSON, otherwise the empty JSON string. Always something the JSONL prompt can
// splice in.
func fallbackInput(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return json.RawMessage(`""`)
	}
	if json.Valid(raw) {
		return raw
	}
	return json.RawMessage(`""`)
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// isFileMutatingTool gates the `acceptEdits` permission-mode branch. Read isn't special-cased here — its in-cwd skip
// handles the latency.
func isFileMutatingTool(name string) bool {
	switch name {
	case "Write", "Edit", "NotebookEdit":
		return true
	}
	return false
}
