// Package llmclassifier is the semantic judge: an LLM call that reads the user's transcript, the merged auto-mode
// policy, and Claude Code's trusted CLAUDE.md snapshot to decide whether the proposed tool invocation should fire.
//
// Per-call pipeline (each phase lives as its own method in `pipeline.go`):
//
//  1. Reject nil/empty input.
//  2. Match the per-project classifier config; bail if disabled.
//  3. Honor the per-session backstop (auto-disable after too many blocks; one-shot ask, then reset).
//  4. Honor user-set permission modes (`bypassPermissions`, `acceptEdits` for file-mutating tools).
//  5. Consult the per-tool evaluator's skip verdict (in-cwd Read, etc.).
//  6. Sanitize the proposed action and read + sanitize the session transcript.
//  7. Build the prompt from the resolved [decider.Env] bundle.
//  8. Resolve the per-project provider (cached).
//  9. Call the provider, parse the verdict, persist counters.
//
// Every failure mode maps to [decider.DecisionSilent] with a populated reason — "we can't classify right now," not
// "block the user." The orchestrator's combiner ignores silent votes, so Claude Code's normal permission flow handles
// the call.
package llmclassifier

import (
	"context"
	"sync"
	"time"

	"claude-auto-permission/internal/cachepath"
	"claude-auto-permission/internal/claudecode/automodepolicy"
	"claude-auto-permission/internal/config"
	"claude-auto-permission/internal/decider"
	configpb "claude-auto-permission/internal/gen/config/v1"
	"claude-auto-permission/internal/hookio"
	"claude-auto-permission/internal/llmclassifier/prompt"
	"claude-auto-permission/internal/llmclassifier/providers"
	"claude-auto-permission/internal/llmclassifier/state"
	"claude-auto-permission/internal/llmclassifier/toolprep"
)

//go:generate go run go.uber.org/mock/mockgen@v0.6.0 -typed -source=classifier.go -destination=mocks/classifier_mock.go -package=mocks

// BuildPromptFn assembles the prompt parts for one classification call. Production wires it to [prompt.Build]; tests
// can pin a fixture closure when they want to assert prompt shape.
type BuildPromptFn func(in prompt.BuildInput, records []prompt.Record, proposed prompt.CallRecord) (prompt.BuildOutput, error)

// ToolRegistry resolves tool names to per-tool [toolprep.Tool] plugins. Extracted as an interface so tests can swap in
// a lighter double than the production [toolprep.Registry].
type ToolRegistry interface {
	Tool(toolName string) toolprep.Tool
}

// ProviderFactory builds a [providers.Provider] from one project's resolved classifier config. The default factory
// delegates to [ProviderFromConfig]; tests can swap it for a fake.
type ProviderFactory func(ctx context.Context, cfg *configpb.LlmClassifierConfig) (providers.Provider, error)

// Decider is the LLM-classifier decider. Reusable across hook invocations — per-cwd config selection and per-project
// provider construction happen on each [Decide] call but are cached.
type Decider struct {
	cfg           *config.Resolver
	tools         ToolRegistry
	provFac       ProviderFactory
	buildPromptFn BuildPromptFn

	// cacheDir threads through to the per-session backstop store and (when enabled) the request/response dump dir.
	cacheDir   string
	dumpDir    string
	stateStore *state.Store

	// providerCache keyed by project root. Bedrock client init is non-trivial (config load, region resolution); reuse
	// across invocations within the same process.
	providerMu    sync.Mutex
	providerCache map[string]cachedProvider
}

// cachedProvider stores the per-project provider plus a sticky construction error so we don't retry every call when the
// AWS config is broken or the binary is missing.
type cachedProvider struct {
	provider providers.Provider
	err      error
}

// New constructs a [Decider] with the production tool registry, provider factory ([ProviderFromConfig]), and prompt
// builder ([prompt.Build]). Tests can swap any of these via the With* setters.
func New(cfg *config.Resolver, cacheDir string) *Decider {
	d := &Decider{
		cfg:           cfg,
		tools:         toolprep.NewDefaultRegistry(),
		provFac:       ProviderFromConfig,
		buildPromptFn: prompt.Build,
		cacheDir:      cacheDir,
		providerCache: map[string]cachedProvider{},
	}
	if cfg != nil && cfg.Proto().GetRuntime().GetDumpLlmClassifier() {
		d.dumpDir = cachepath.DumpsDir(cacheDir)
	}
	d.stateStore = state.New(cachepath.SessionsDir(cacheDir))
	return d
}

// WithToolRegistry replaces the per-tool plugin registry. Test seam for substituting a lighter double than the
// production registry.
func (d *Decider) WithToolRegistry(r ToolRegistry) *Decider {
	d.tools = r
	return d
}

// WithProviderFactory replaces the provider constructor. Test seam for swapping in fake providers without going through
// Bedrock.
func (d *Decider) WithProviderFactory(f ProviderFactory) *Decider {
	d.provFac = f
	return d
}

// WithBuildPromptFn replaces the prompt builder. Test seam for asserting the prompt input or substituting a fixture.
func (d *Decider) WithBuildPromptFn(f BuildPromptFn) *Decider {
	d.buildPromptFn = f
	return d
}

func (*Decider) Name() string { return decider.NameLlmClassifier }

// Decide runs the per-call pipeline. The body is in [Decider.runPipeline] — one phase per concept, each in its own
// method, threaded through a small `step` value.
func (d *Decider) Decide(ctx context.Context, in *hookio.HookInput, env decider.Env) decider.Result {
	return d.runPipeline(ctx, in, env)
}

// providerFor returns the provider for projectRoot, building it on first need and caching the result (or the
// construction error) for the rest of this process's lifetime.
func (d *Decider) providerFor(ctx context.Context, projectRoot string, pcfg *configpb.LlmClassifierConfig) (providers.Provider, error) {
	d.providerMu.Lock()
	defer d.providerMu.Unlock()
	if cached, ok := d.providerCache[projectRoot]; ok {
		return cached.provider, cached.err
	}
	p, err := d.provFac(ctx, pcfg)
	d.providerCache[projectRoot] = cachedProvider{provider: p, err: err}
	return p, err
}

// buildPrompt loads the auto-mode policy and renders the prompt for this call. The policy load is per-cwd
// (settings.local.json reads cwd-relative); the resulting cache key folds in every reachable settings path so different
// projects don't share entries.
func (d *Decider) buildPrompt(ctx context.Context, env decider.Env, classCfg Config, records []prompt.Record, proposed prompt.CallRecord) (prompt.BuildOutput, error) {
	loader := &automodepolicy.Loader{
		CacheDir:      cachepath.AutoModePolicyCacheDir(d.cacheDir),
		Ttl:           classCfg.AutoModePolicyTtl,
		SettingsPaths: env.PermScope.Candidates,
		Cwd:           env.Cwd,
	}
	policy := loader.LoadOrDefaults(ctx)

	return d.buildPromptFn(prompt.BuildInput{
		Policy:       policy,
		Cwd:          env.Cwd,
		ProjectRoot:  env.ProjectRoot,
		WorkingDirs:  env.PermScope.WorkingDirs,
		DenyRules:    env.PermScope.DenyRules,
		Instructions: env.Instructions,
	}, records, proposed)
}

// ask wraps [decider.Ask] and stamps the call's wall-clock cost.
func (d *Decider) ask(reason string, start time.Time) decider.Result {
	r := decider.Ask(reason)
	r.LatencyMs = ms(start)
	return r
}

// silent wraps [decider.Silent] and stamps the call's wall-clock cost. Meta is omitted — pre-provider silent paths
// don't have a model to report; post-provider silent paths populate Meta explicitly.
func (d *Decider) silent(reason string, start time.Time) decider.Result {
	r := decider.Silent(reason)
	r.LatencyMs = ms(start)
	return r
}

// passthrough wraps [decider.Passthrough] and stamps the call's wall-clock cost.
func (d *Decider) passthrough(reason string, start time.Time) decider.Result {
	r := decider.Passthrough(reason)
	r.LatencyMs = ms(start)
	return r
}

// onError chooses the verdict for an infrastructure-failure path based on the per-project [OnClassifierError] knob.
// Default (`Passthrough`) overrides any peer Allow but emits no wire output; `Ask` forces a Claude Code permission
// prompt instead. Reason is required — it surfaces in the decision log either way.
func (d *Decider) onError(reason string, start time.Time, mode OnClassifierError) decider.Result {
	if mode == OnClassifierErrorAsk {
		return d.ask(reason, start)
	}
	return d.passthrough(reason, start)
}

func meta(p providers.Provider) map[string]string {
	return map[string]string{
		"provider": p.Name(),
		"model":    p.Model(),
	}
}

func ms(start time.Time) int {
	return int(time.Since(start) / time.Millisecond)
}
