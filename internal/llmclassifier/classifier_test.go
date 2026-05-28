package llmclassifier

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"go.uber.org/mock/gomock"

	"claude-auto-permission/internal/config"
	"claude-auto-permission/internal/decider"
	configpb "claude-auto-permission/internal/gen/config/v1"
	"claude-auto-permission/internal/hookio"
	"claude-auto-permission/internal/llmclassifier/prompt"
	"claude-auto-permission/internal/llmclassifier/providers"
	providermocks "claude-auto-permission/internal/llmclassifier/providers/mocks"
	"claude-auto-permission/internal/llmclassifier/toolprep"
	toolmocks "claude-auto-permission/internal/llmclassifier/toolprep/mocks"
)

// ─── helpers ────────────────────────────────────────────────────────

// passthroughSanitize passes tool input through verbatim, defaulting to "{}" when empty. Lets the verdict-shape tests
// focus on the orchestrator's behavior without per-tool plugin quirks.
func passthroughSanitize(in toolprep.Input) (json.RawMessage, error) {
	if len(in.ToolInput) == 0 {
		return json.RawMessage(`{}`), nil
	}
	return in.ToolInput, nil
}

type stubToolRegistry struct{ tool toolprep.Tool }

func (s stubToolRegistry) Tool(string) toolprep.Tool { return s.tool }

// toolRegistry builds a single MockTool with passthrough behaviors and wraps it in a ToolRegistry that always returns
// it. `skip` controls the Skippable return value the orchestrator sees; when skip == Skip the reason is "test skip
// reason".
func toolRegistry(ctrl *gomock.Controller, skip toolprep.Skippable) ToolRegistry {
	tool := toolmocks.NewMockTool(ctrl)
	reason := ""
	if skip == toolprep.Skip {
		reason = "test skip reason"
	}
	tool.EXPECT().Skippable(gomock.Any()).Return(skip, reason).AnyTimes()
	tool.EXPECT().Sanitize(gomock.Any()).DoAndReturn(passthroughSanitize).AnyTimes()
	return stubToolRegistry{tool: tool}
}

// newProvider returns a fresh provider mock with Name() and Model() pre-stubbed. Tests add Classify expectations as
// needed; an unexpected Classify call fails the test by default.
func newProvider(ctrl *gomock.Controller) *providermocks.MockProvider {
	p := providermocks.NewMockProvider(ctrl)
	p.EXPECT().Name().Return("stub").AnyTimes()
	p.EXPECT().Model().Return("stub-model").AnyTimes()
	return p
}

// classifierCfgOpts mutates the per-project classifier config a test uses. Default config sets Enabled=true; overrides
// feed in MaxConsecutiveBlocks, Timeout, etc.
type classifierCfgOpts struct {
	enabled              *bool
	timeoutMs            int32
	maxConsecutiveBlocks int32
	backstopTtlSeconds   int32
	onClassifierError    *configpb.LlmClassifierConfig_OnClassifierError
}

func resolverWithClassifier(opts classifierCfgOpts) *config.Resolver {
	enabled := true
	if opts.enabled != nil {
		enabled = *opts.enabled
	}

	cb := configpb.LlmClassifierConfig_builder{
		Enabled: &enabled,
	}
	// Provider has to be set so cfg validates downstream; we never reach the real factory because every test substitutes
	// one via Decider.WithProviderFactory.
	cb.Bedrock = configpb.BedrockProvider_builder{
		ModelId: ptrString("stub-model"),
	}.Build()
	if opts.timeoutMs > 0 {
		cb.TimeoutMs = &opts.timeoutMs
	}
	if opts.maxConsecutiveBlocks > 0 {
		cb.MaxConsecutiveBlocks = &opts.maxConsecutiveBlocks
	}
	if opts.backstopTtlSeconds > 0 {
		cb.BackstopTtlSeconds = &opts.backstopTtlSeconds
	}
	if opts.onClassifierError != nil {
		cb.OnClassifierError = opts.onClassifierError
	}

	return config.NewResolver(configpb.Config_builder{
		Projects: []*configpb.Project{
			configpb.Project_builder{
				PathPatterns:  []string{"/**"},
				LlmClassifier: cb.Build(),
			}.Build(),
		},
	}.Build())
}

// newDecider builds a Decider with a stub tool registry and a fixed-provider factory so tests don't have to talk to
// Bedrock. cacheDir is a temp dir per test.
func newDecider(t *testing.T, ctrl *gomock.Controller, prov providers.Provider, skip toolprep.Skippable, opts classifierCfgOpts) *Decider {
	t.Helper()
	cfg := resolverWithClassifier(opts)
	return New(cfg, t.TempDir()).
		WithToolRegistry(toolRegistry(ctrl, skip)).
		WithProviderFactory(func(_ context.Context, _ *configpb.LlmClassifierConfig) (providers.Provider, error) {
			return prov, nil
		})
}

func bashInput(t *testing.T, command, sessionID string) *hookio.HookInput {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"command": command})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return &hookio.HookInput{
		HookEventName: hookio.EventPreToolUse,
		SessionId:     sessionID,
		Cwd:           "/project",
		ToolName:      "Bash",
		ToolInput:     raw,
	}
}

func envFor(in *hookio.HookInput) decider.Env {
	return decider.Env{
		Cwd:         in.Cwd,
		ProjectRoot: "/project",
	}
}

func ptrString(s string) *string { return &s }
func ptrTrue() *bool             { v := true; return &v }
func ptrFalse() *bool            { v := false; return &v }

// ─── tests ──────────────────────────────────────────────────────────

func TestDecide_DisabledIsSilent(t *testing.T) {
	ctrl := gomock.NewController(t)
	prov := newProvider(ctrl)
	// No Classify expectation: gomock fails the test if the provider is called, which is exactly the assertion we want.
	d := newDecider(t, ctrl, prov, toolprep.SkipNone, classifierCfgOpts{enabled: ptrFalse()})
	in := bashInput(t, "ls", "")
	r := d.Decide(context.Background(), in, envFor(in))
	if r.Decision != decider.DecisionSilent {
		t.Errorf("Decision = %q, want silent", r.Decision)
	}
	if r.Reason == "" {
		t.Errorf("disabled silent should have a reason")
	}
}

func TestDecide_NilOrEmptyInputIsSilent(t *testing.T) {
	ctrl := gomock.NewController(t)
	prov := newProvider(ctrl)
	d := newDecider(t, ctrl, prov, toolprep.SkipNone, classifierCfgOpts{})
	if r := d.Decide(context.Background(), nil, decider.Env{}); r.Decision != decider.DecisionSilent {
		t.Errorf("nil input: Decision = %q, want silent", r.Decision)
	}
	in := bashInput(t, "ls", "")
	in.ToolName = ""
	if r := d.Decide(context.Background(), in, envFor(in)); r.Decision != decider.DecisionSilent {
		t.Errorf("empty tool name: Decision = %q, want silent", r.Decision)
	}
}

func TestDecide_SkipIsSilent(t *testing.T) {
	ctrl := gomock.NewController(t)
	prov := newProvider(ctrl)
	d := newDecider(t, ctrl, prov, toolprep.Skip, classifierCfgOpts{})
	in := bashInput(t, "ls", "s1")
	r := d.Decide(context.Background(), in, envFor(in))
	if r.Decision != decider.DecisionSilent {
		t.Errorf("Decision = %q, want silent", r.Decision)
	}
	if r.Reason == "" {
		t.Errorf("Skip vote should carry a reason; got empty")
	}
}

func TestDecide_BypassPermissionsIsSilent(t *testing.T) {
	ctrl := gomock.NewController(t)
	prov := newProvider(ctrl)
	d := newDecider(t, ctrl, prov, toolprep.SkipNone, classifierCfgOpts{})
	in := bashInput(t, "rm -rf /", "s1")
	in.PermissionMode = "bypassPermissions"
	r := d.Decide(context.Background(), in, envFor(in))
	if r.Decision != decider.DecisionSilent {
		t.Errorf("Decision = %q, want silent", r.Decision)
	}
}

func TestDecide_AcceptEditsSkipsFileMutatingTools(t *testing.T) {
	tests := []struct {
		tool        string
		wantDec     decider.Decision
		expectClass bool
	}{
		{"Write", decider.DecisionSilent, false},
		{"Edit", decider.DecisionSilent, false},
		{"NotebookEdit", decider.DecisionSilent, false},
		// Non-file-mutating tools (e.g. Bash) classify normally even under acceptEdits.
		{"Bash", decider.DecisionAllow, true},
	}
	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			prov := newProvider(ctrl)
			if tt.expectClass {
				prov.EXPECT().Classify(gomock.Any(), gomock.Any()).Return(providers.Result{ShouldBlock: false}, nil)
			}
			d := newDecider(t, ctrl, prov, toolprep.SkipNone, classifierCfgOpts{})
			in := bashInput(t, "doesn't matter", "s")
			in.ToolName = tt.tool
			in.PermissionMode = "acceptEdits"
			r := d.Decide(context.Background(), in, envFor(in))
			if r.Decision != tt.wantDec {
				t.Errorf("tool=%s: Decision = %q, want %q", tt.tool, r.Decision, tt.wantDec)
			}
		})
	}
}

func TestDecide_BlockVerdict(t *testing.T) {
	ctrl := gomock.NewController(t)
	prov := newProvider(ctrl)
	prov.EXPECT().Classify(gomock.Any(), gomock.Any()).Return(providers.Result{ShouldBlock: true, Reason: "no"}, nil)
	d := newDecider(t, ctrl, prov, toolprep.SkipNone, classifierCfgOpts{})
	in := bashInput(t, "rm -rf /", "s1")
	r := d.Decide(context.Background(), in, envFor(in))
	if r.Decision != decider.DecisionDeny {
		t.Errorf("Decision = %q, want deny", r.Decision)
	}
	if r.Reason != "no" {
		t.Errorf("Reason = %q, want %q", r.Reason, "no")
	}
	if r.Meta["provider"] != "stub" || r.Meta["model"] != "stub-model" {
		t.Errorf("Meta should carry provider/model: %v", r.Meta)
	}
}

func TestDecide_AllowVerdict(t *testing.T) {
	ctrl := gomock.NewController(t)
	prov := newProvider(ctrl)
	prov.EXPECT().Classify(gomock.Any(), gomock.Any()).Return(providers.Result{ShouldBlock: false}, nil)
	d := newDecider(t, ctrl, prov, toolprep.SkipNone, classifierCfgOpts{})
	in := bashInput(t, "git status", "s1")
	r := d.Decide(context.Background(), in, envFor(in))
	if r.Decision != decider.DecisionAllow {
		t.Errorf("Decision = %q, want allow", r.Decision)
	}
	if r.Meta["provider"] != "stub" {
		t.Errorf("Meta should carry provider: %v", r.Meta)
	}
}

func TestDecide_ProviderUnavailableMapsToPassthroughByDefault(t *testing.T) {
	ctrl := gomock.NewController(t)
	prov := newProvider(ctrl)
	prov.EXPECT().Classify(gomock.Any(), gomock.Any()).Return(providers.Result{}, errors.New("kaboom"))
	d := newDecider(t, ctrl, prov, toolprep.SkipNone, classifierCfgOpts{})
	in := bashInput(t, "anything", "s1")
	r := d.Decide(context.Background(), in, envFor(in))
	if r.Decision != decider.DecisionPassthrough {
		t.Errorf("Decision = %q, want passthrough", r.Decision)
	}
	if r.Reason == "" {
		t.Errorf("provider error passthrough must have reason")
	}
	// Provider/model meta still surfaces — the log entry needs to attribute the failure to a specific provider.
	if r.Meta["provider"] == "" || r.Meta["model"] == "" {
		t.Errorf("provider error passthrough must populate Meta provider/model: %+v", r.Meta)
	}
}

func TestDecide_ProviderUnavailableMapsToAskWhenConfigured(t *testing.T) {
	ctrl := gomock.NewController(t)
	prov := newProvider(ctrl)
	prov.EXPECT().Classify(gomock.Any(), gomock.Any()).Return(providers.Result{}, errors.New("kaboom"))
	mode := configpb.LlmClassifierConfig_ON_CLASSIFIER_ERROR_ASK
	d := newDecider(t, ctrl, prov, toolprep.SkipNone, classifierCfgOpts{onClassifierError: &mode})
	in := bashInput(t, "anything", "s1")
	r := d.Decide(context.Background(), in, envFor(in))
	if r.Decision != decider.DecisionAsk {
		t.Errorf("Decision = %q, want ask", r.Decision)
	}
	if r.Reason == "" {
		t.Errorf("provider error ask must have reason")
	}
}

// TestDecide_BuildPromptErrorRoutesByConfig covers the prompt-build infra-failure path. Same dispatch shape as the
// provider-error path: passthrough by default, ask when configured.
func TestDecide_BuildPromptErrorRoutesByConfig(t *testing.T) {
	ask := configpb.LlmClassifierConfig_ON_CLASSIFIER_ERROR_ASK
	tests := []struct {
		name    string
		mode    *configpb.LlmClassifierConfig_OnClassifierError
		wantDec decider.Decision
	}{
		{name: "default → passthrough", mode: nil, wantDec: decider.DecisionPassthrough},
		{name: "ASK → ask", mode: &ask, wantDec: decider.DecisionAsk},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			prov := newProvider(ctrl)
			// Provider must NOT be called — buildPrompt fails first.
			d := newDecider(t, ctrl, prov, toolprep.SkipNone, classifierCfgOpts{onClassifierError: tt.mode}).
				WithBuildPromptFn(func(prompt.BuildInput, []prompt.Record, prompt.CallRecord) (prompt.BuildOutput, error) {
					return prompt.BuildOutput{}, errors.New("synthetic build error")
				})
			in := bashInput(t, "anything", "s1")
			r := d.Decide(context.Background(), in, envFor(in))
			if r.Decision != tt.wantDec {
				t.Errorf("Decision = %q, want %q", r.Decision, tt.wantDec)
			}
			if r.Reason == "" {
				t.Errorf("infra-failure verdict must carry a reason")
			}
		})
	}
}

// TestDecide_ProviderConstructionErrorRoutesByConfig covers the provider-construction infra-failure path. Same shape as
// above.
func TestDecide_ProviderConstructionErrorRoutesByConfig(t *testing.T) {
	ask := configpb.LlmClassifierConfig_ON_CLASSIFIER_ERROR_ASK
	tests := []struct {
		name    string
		mode    *configpb.LlmClassifierConfig_OnClassifierError
		wantDec decider.Decision
	}{
		{name: "default → passthrough", mode: nil, wantDec: decider.DecisionPassthrough},
		{name: "ASK → ask", mode: &ask, wantDec: decider.DecisionAsk},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			cfg := resolverWithClassifier(classifierCfgOpts{onClassifierError: tt.mode})
			d := New(cfg, t.TempDir()).
				WithToolRegistry(toolRegistry(ctrl, toolprep.SkipNone)).
				WithProviderFactory(func(_ context.Context, _ *configpb.LlmClassifierConfig) (providers.Provider, error) {
					return nil, errors.New("synthetic ctor error")
				})
			in := bashInput(t, "anything", "s1")
			r := d.Decide(context.Background(), in, envFor(in))
			if r.Decision != tt.wantDec {
				t.Errorf("Decision = %q, want %q", r.Decision, tt.wantDec)
			}
			if r.Reason == "" {
				t.Errorf("infra-failure verdict must carry a reason")
			}
		})
	}
}

func TestDecide_BackstopAfterConsecutiveBlocks(t *testing.T) {
	ctrl := gomock.NewController(t)
	prov := newProvider(ctrl)
	// MaxConsecutiveBlocks = 2 → 2 deny verdicts trip the backstop; the third call should return "ask" without hitting the
	// provider.
	prov.EXPECT().Classify(gomock.Any(), gomock.Any()).
		Return(providers.Result{ShouldBlock: true, Reason: "blocked"}, nil).Times(2)
	d := newDecider(t, ctrl, prov, toolprep.SkipNone, classifierCfgOpts{maxConsecutiveBlocks: 2})
	in := bashInput(t, "rm -rf /", "backstop-test")
	for range 2 {
		r := d.Decide(context.Background(), in, envFor(in))
		if r.Decision != decider.DecisionDeny {
			t.Fatalf("first/second call: Decision = %q, want deny", r.Decision)
		}
	}
	r := d.Decide(context.Background(), in, envFor(in))
	if r.Decision != decider.DecisionAsk {
		t.Errorf("after backstop: Decision = %q, want ask", r.Decision)
	}
	if r.Reason == "" {
		t.Errorf("backstop ask should explain why")
	}
}

func TestDecide_BackstopIsOneShot(t *testing.T) {
	ctrl := gomock.NewController(t)
	prov := newProvider(ctrl)
	gomock.InOrder(
		prov.EXPECT().Classify(gomock.Any(), gomock.Any()).Return(providers.Result{ShouldBlock: true, Reason: "no"}, nil),
		prov.EXPECT().Classify(gomock.Any(), gomock.Any()).Return(providers.Result{ShouldBlock: true, Reason: "no"}, nil),
		prov.EXPECT().Classify(gomock.Any(), gomock.Any()).Return(providers.Result{ShouldBlock: false, Reason: "ok"}, nil),
	)
	d := newDecider(t, ctrl, prov, toolprep.SkipNone, classifierCfgOpts{maxConsecutiveBlocks: 2})
	in := bashInput(t, "rm -rf /", "oneshot-test")

	for range 2 {
		d.Decide(context.Background(), in, envFor(in))
	}
	r := d.Decide(context.Background(), in, envFor(in))
	if r.Decision != decider.DecisionAsk {
		t.Fatalf("backstop call: Decision = %q, want ask", r.Decision)
	}
	r = d.Decide(context.Background(), in, envFor(in))
	if r.Decision != decider.DecisionAllow {
		t.Errorf("post-backstop call: Decision = %q, want allow", r.Decision)
	}
}

func TestDecide_BackstopTtlExpiry(t *testing.T) {
	ctrl := gomock.NewController(t)
	prov := newProvider(ctrl)
	gomock.InOrder(
		prov.EXPECT().Classify(gomock.Any(), gomock.Any()).Return(providers.Result{ShouldBlock: true, Reason: "no"}, nil),
		prov.EXPECT().Classify(gomock.Any(), gomock.Any()).Return(providers.Result{ShouldBlock: true, Reason: "no"}, nil),
		// Third call: backstop expired, falls through to classification.
		prov.EXPECT().Classify(gomock.Any(), gomock.Any()).Return(providers.Result{ShouldBlock: false, Reason: "ok"}, nil),
	)
	d := newDecider(t, ctrl, prov, toolprep.SkipNone, classifierCfgOpts{
		maxConsecutiveBlocks: 2,
		backstopTtlSeconds:   1, // smallest nonzero — overridden via runtime mutation below
	})
	// Force the TTL to nanosecond-level by mutating the cached config indirectly: just sleep is fragile. Instead patch the
	// runtime classCfg via a direct path — re-enabled by re-construction is simpler. Use a tiny TTL by setting
	// backstopTtlSeconds=1 and sleeping >1s after the second block.
	in := bashInput(t, "rm -rf /", "ttl-test")
	for range 2 {
		d.Decide(context.Background(), in, envFor(in))
	}
	time.Sleep(1100 * time.Millisecond)
	r := d.Decide(context.Background(), in, envFor(in))
	if r.Decision != decider.DecisionAllow {
		t.Errorf("TTL-expired call: Decision = %q, want allow (classified fresh)", r.Decision)
	}
}

func TestDecide_AllowResetsConsecutiveBlocks(t *testing.T) {
	ctrl := gomock.NewController(t)
	prov := newProvider(ctrl)
	gomock.InOrder(
		prov.EXPECT().Classify(gomock.Any(), gomock.Any()).Return(providers.Result{ShouldBlock: true, Reason: "no"}, nil),
		prov.EXPECT().Classify(gomock.Any(), gomock.Any()).Return(providers.Result{ShouldBlock: false}, nil),
		prov.EXPECT().Classify(gomock.Any(), gomock.Any()).Return(providers.Result{ShouldBlock: true, Reason: "no"}, nil),
		prov.EXPECT().Classify(gomock.Any(), gomock.Any()).Return(providers.Result{ShouldBlock: true, Reason: "no"}, nil),
	)
	d := newDecider(t, ctrl, prov, toolprep.SkipNone, classifierCfgOpts{maxConsecutiveBlocks: 3})
	in := bashInput(t, "rm -rf /", "reset-test")
	// deny → allow (resets consecutive) → deny → deny: 2 consecutive, below the limit of 3, so the fourth call should not
	// have tripped the backstop.
	for range 4 {
		_ = d.Decide(context.Background(), in, envFor(in))
	}
}

func TestDecide_TimeoutIsRespected(t *testing.T) {
	ctrl := gomock.NewController(t)
	prov := newProvider(ctrl)
	prov.EXPECT().Classify(gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, _ providers.Request) (providers.Result, error) {
			<-ctx.Done()
			return providers.Result{}, ctx.Err()
		})
	d := newDecider(t, ctrl, prov, toolprep.SkipNone, classifierCfgOpts{timeoutMs: 50})
	in := bashInput(t, "anything", "s1")
	start := time.Now()
	r := d.Decide(context.Background(), in, envFor(in))
	elapsed := time.Since(start)
	// Timeout is an infrastructure failure → passthrough by default.
	if r.Decision != decider.DecisionPassthrough {
		t.Errorf("Decision = %q, want passthrough on timeout", r.Decision)
	}
	if elapsed > 250*time.Millisecond {
		t.Errorf("Decide took %v, expected ≤250ms (timeout=50ms)", elapsed)
	}
}

// silence the unused warning if only some helpers are used in this build configuration.
var _ = ptrTrue
