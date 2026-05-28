package bedrock

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"go.uber.org/mock/gomock"

	"claude-auto-permission/internal/llmclassifier/providers"
	bedrockmocks "claude-auto-permission/internal/llmclassifier/providers/bedrock/mocks"
)

// userMessages is the helper that turns (prefix, body) into the Converse messages slice. The CLAUDE.md prefix becomes a
// separate user-role message before the transcript when set, mirroring the reference impl's prefixMessages array.
func TestUserMessages(t *testing.T) {
	t.Run("no prefix yields one message", func(t *testing.T) {
		got := userMessages("", "body")
		if len(got) != 1 {
			t.Fatalf("got %d messages, want 1", len(got))
		}
	})
	t.Run("prefix yields two messages, prefix first", func(t *testing.T) {
		got := userMessages("CLAUDE.md content", "body")
		if len(got) != 2 {
			t.Fatalf("got %d messages, want 2", len(got))
		}
		// Both must carry user role.
		for i, m := range got {
			if m.Role != types.ConversationRoleUser {
				t.Errorf("message %d role = %v, want user", i, m.Role)
			}
		}
		// First message contains the prefix text; second contains body.
		if !contentContains(got[0].Content, "CLAUDE.md content") {
			t.Errorf("first message missing prefix")
		}
		if !contentContains(got[1].Content, "body") {
			t.Errorf("second message missing body")
		}
	})
}

func contentContains(blocks []types.ContentBlock, want string) bool {
	for _, b := range blocks {
		if t, ok := b.(*types.ContentBlockMemberText); ok {
			if strings.Contains(t.Value, want) {
				return true
			}
		}
	}
	return false
}

// xmlTextOutput wraps the given text as a Converse output with one text content block — what real Bedrock returns for
// XML modes (no tool_use, just text). Reduces test boilerplate; every XML-format test feeds back the same shape.
func xmlTextOutput(text string) *bedrockruntime.ConverseOutput {
	return &bedrockruntime.ConverseOutput{
		Output: &types.ConverseOutputMemberMessage{
			Value: types.Message{
				Role:    types.ConversationRoleAssistant,
				Content: []types.ContentBlock{&types.ContentBlockMemberText{Value: text}},
			},
		},
	}
}

func TestParseXMLBlock(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantOK  bool
		wantBlk bool
	}{
		{name: "yes closed", input: "<block>yes</block>", wantOK: true, wantBlk: true},
		{name: "no closed", input: "<block>no</block>", wantOK: true, wantBlk: false},
		{
			// Stop sequence aborts before the closing tag — what real stage-1 responses produce in TwoStageBoth on "no".
			name: "no without closing tag", input: "<block>no", wantOK: true, wantBlk: false,
		},
		{name: "yes with reason", input: "<block>yes</block><reason>foo</reason>", wantOK: true, wantBlk: true},
		{name: "uppercase YES", input: "<block>YES</block>", wantOK: true, wantBlk: true},
		{name: "leading whitespace", input: "<block>  yes  </block>", wantOK: true, wantBlk: true},
		{
			// Tags inside <thinking> must not match: stripThinking removes them before regex scan.
			name:    "verdict inside thinking ignored",
			input:   "<thinking><block>yes</block></thinking><block>no</block>",
			wantOK:  true,
			wantBlk: false,
		},
		{
			// Unterminated thinking tail: model started thinking, hit max_tokens before closing. Strip the tail then fail-closed
			// (no remaining <block>).
			name:   "unterminated thinking",
			input:  "<thinking>here we go and...",
			wantOK: false,
		},
		{name: "no verdict tag at all", input: "Looking at the situation...", wantOK: false},
		{name: "empty", input: "", wantOK: false},
		{
			// Defense check: <block>maybe</block> is not parseable. The regex demands yes|no.
			name:   "invalid verdict word",
			input:  "<block>maybe</block>",
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotBlk, gotOK := parseXMLBlock(tt.input)
			if gotOK != tt.wantOK {
				t.Errorf("ok = %v, want %v", gotOK, tt.wantOK)
			}
			if gotBlk != tt.wantBlk {
				t.Errorf("block = %v, want %v", gotBlk, tt.wantBlk)
			}
		})
	}
}

func TestParseXMLReason(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "single line", input: "<block>yes</block><reason>boom</reason>", want: "boom"},
		{name: "trims whitespace", input: "<reason>\n\thello\n</reason>", want: "hello"},
		{name: "first match wins",
			input: "<reason>first</reason><reason>second</reason>", want: "first"},
		{name: "ignores reason inside thinking",
			input: "<thinking><reason>secret</reason></thinking><reason>real</reason>",
			want:  "real"},
		{name: "absent", input: "<block>no</block>", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseXMLReason(tt.input); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRewriteSystemForXml(t *testing.T) {
	t.Run("replaces canonical line", func(t *testing.T) {
		// The canonical instruction line in the bundled system template is what we substitute. If the prompt template changes
		// wording, systemPromptOutputLine and system.txt must be updated in lockstep — see the comment on the constant. This
		// test catches drift.
		got := rewriteSystemForXml("preamble " + systemPromptOutputLine + " trailer")
		if strings.Contains(got, systemPromptOutputLine) {
			t.Error("expected canonical line to be replaced")
		}
		if !strings.Contains(got, "<block>yes</block>") {
			t.Errorf("missing XML format block; got: %q", got)
		}
	})
	t.Run("appends when canonical missing", func(t *testing.T) {
		got := rewriteSystemForXml("system without the marker")
		if !strings.HasSuffix(got, xmlOutputFormat) {
			t.Errorf("xml format not appended; tail: %q", got[len(got)-50:])
		}
	})
}

// providerForMode constructs a Provider with the given two-stage mode and a fresh mock client. Returns the mock so the
// test can set up EXPECT() calls.
func providerForMode(t *testing.T, mode TwoStageMode) (*Provider, *bedrockmocks.Mockconverser) {
	t.Helper()
	ctrl := gomock.NewController(t)
	mc := bedrockmocks.NewMockconverser(ctrl)
	return &Provider{
		cfg:    Config{ModelId: "test-model", TwoStage: mode},
		client: mc,
	}, mc
}

func TestProvider_TwoStageBoth_AllowFastPath(t *testing.T) {
	// Stage-1 emits <block>no — stop sequence aborts before the closing tag, but our regex still parses it. The test mock
	// has only a single Converse expectation; if the provider unexpectedly tries to make a second call (e.g. an escalation
	// that shouldn't happen), gomock fails.
	p, mc := providerForMode(t, TwoStageBoth)
	mc.EXPECT().
		Converse(gomock.Any(), gomock.Any()).
		Return(xmlTextOutput("<block>no"), nil)

	res, err := p.Classify(context.Background(), providers.Request{
		SystemPrompt: "...prompt...",
		UserPrompt:   "transcript body",
	})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if res.ShouldBlock {
		t.Error("ShouldBlock = true, want false")
	}
}

func TestProvider_TwoStageBoth_BlockEscalates(t *testing.T) {
	// Stage 1 emits <block>yes — orchestrator must escalate to a stage-2 XML call (not forced tool use any more — we share
	// the XML format across stages so stage 2 hits the cached prefix).
	p, mc := providerForMode(t, TwoStageBoth)
	gomock.InOrder(
		mc.EXPECT().
			Converse(gomock.Any(), gomock.Any()).
			Return(xmlTextOutput("<block>yes</block>"), nil),
		mc.EXPECT().
			Converse(gomock.Any(), gomock.Any()).
			Return(xmlTextOutput("<thinking>this is curl-pipe-shell</thinking><block>yes</block><reason>code from external source</reason>"), nil),
	)

	res, err := p.Classify(context.Background(), providers.Request{
		SystemPrompt: "...",
		UserPrompt:   "transcript",
	})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if !res.ShouldBlock {
		t.Error("ShouldBlock = false, want true")
	}
	if res.Reason != "code from external source" {
		t.Errorf("Reason = %q", res.Reason)
	}
}

func TestProvider_TwoStageBoth_Stage1ParseFailEscalates(t *testing.T) {
	// Stage 1 emits a malformed verdict (no <block> tag). Per the design choice, TwoStageBoth escalates to stage 2 rather
	// than blocking blindly. Stage 2 returns a clean XML verdict.
	p, mc := providerForMode(t, TwoStageBoth)
	gomock.InOrder(
		mc.EXPECT().
			Converse(gomock.Any(), gomock.Any()).
			Return(xmlTextOutput("I'll think about this..."), nil),
		mc.EXPECT().
			Converse(gomock.Any(), gomock.Any()).
			Return(xmlTextOutput("<block>no</block>"), nil),
	)

	res, err := p.Classify(context.Background(), providers.Request{
		UserPrompt: "anything",
	})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if res.ShouldBlock {
		t.Error("expected escalation to land on allow per stage 2")
	}
}

func TestProvider_TwoStageFast_BlockTerminal(t *testing.T) {
	// Fast mode: stage 1 verdict (and reason + tier) is final. Stop sequence is dropped, max_tokens is bumped, so the
	// model can emit the full reason+tier on the same response.
	p, mc := providerForMode(t, TwoStageFast)
	mc.EXPECT().
		Converse(gomock.Any(), gomock.Any()).
		Return(xmlTextOutput("<block>yes</block><reason>destructive shell pipe</reason><tier>hard_deny</tier>"), nil)

	res, err := p.Classify(context.Background(), providers.Request{UserPrompt: "x"})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if !res.ShouldBlock {
		t.Error("expected block in fast mode")
	}
	if res.Reason != "destructive shell pipe" {
		t.Errorf("Reason = %q", res.Reason)
	}
}

func TestProvider_TwoStageFast_BlockNoReasonGetsDefault(t *testing.T) {
	// Fast mode + the model forgot to emit <reason>. The provider fills in a fallback so the user-visible deny message
	// isn't empty.
	p, mc := providerForMode(t, TwoStageFast)
	mc.EXPECT().
		Converse(gomock.Any(), gomock.Any()).
		Return(xmlTextOutput("<block>yes</block>"), nil)

	res, err := p.Classify(context.Background(), providers.Request{UserPrompt: "x"})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if !res.ShouldBlock {
		t.Error("expected block")
	}
	if res.Reason == "" {
		t.Error("expected fallback reason")
	}
}

func TestProvider_TwoStageFast_Allow(t *testing.T) {
	p, mc := providerForMode(t, TwoStageFast)
	mc.EXPECT().
		Converse(gomock.Any(), gomock.Any()).
		Return(xmlTextOutput("<block>no</block>"), nil)

	res, err := p.Classify(context.Background(), providers.Request{UserPrompt: "x"})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if res.ShouldBlock {
		t.Error("expected allow")
	}
}

func TestProvider_TwoStageFast_ParseFailUnavailable(t *testing.T) {
	// Fast mode: no parseable verdict means we have nothing to return. The provider surfaces an UnavailableParse error so
	// the orchestrator falls through (silent + unavailable=true), rather than fabricating a verdict.
	p, mc := providerForMode(t, TwoStageFast)
	mc.EXPECT().
		Converse(gomock.Any(), gomock.Any()).
		Return(xmlTextOutput("unparseable model output"), nil)

	_, err := p.Classify(context.Background(), providers.Request{UserPrompt: "x"})
	var perr *providers.Error
	if !errors.As(err, &perr) {
		t.Fatalf("got %v, want *providers.Error", err)
	}
	if perr.Reason != providers.UnavailableParse {
		t.Errorf("Reason = %q, want UnavailableParse", perr.Reason)
	}
}

func TestProvider_TwoStageThinking_HappyPath(t *testing.T) {
	// Thinking mode: single XML chain-of-thought call. No stage 1.
	p, mc := providerForMode(t, TwoStageThinking)
	mc.EXPECT().
		Converse(gomock.Any(), gomock.Any()).
		Return(xmlTextOutput("<thinking>...</thinking><block>yes</block><reason>creds in URL</reason>"), nil)

	res, err := p.Classify(context.Background(), providers.Request{
		SystemPrompt: "...",
		UserPrompt:   "x",
	})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if !res.ShouldBlock {
		t.Error("expected block")
	}
	if res.Reason != "creds in URL" {
		t.Errorf("Reason = %q", res.Reason)
	}
}

func TestProvider_TwoStageBoth_StreamErrorMaps(t *testing.T) {
	// SDK error on stage 1's Converse call should map to UnavailableProviderErr, not crash the pipeline.
	p, mc := providerForMode(t, TwoStageBoth)
	mc.EXPECT().
		Converse(gomock.Any(), gomock.Any()).
		Return(nil, errors.New("ThrottlingException"))

	_, err := p.Classify(context.Background(), providers.Request{UserPrompt: "x"})
	var perr *providers.Error
	if !errors.As(err, &perr) {
		t.Fatalf("got %v, want *providers.Error", err)
	}
	if perr.Reason != providers.UnavailableProviderErr {
		t.Errorf("Reason = %q, want UnavailableProviderErr", perr.Reason)
	}
}

func TestProvider_TwoStage_PromptTooLongMaps(t *testing.T) {
	// Bedrock's prompt-too-long ValidationException is recognized the same way in stage 1 as in single-stage. classifyErr
	// surfaces UnavailableTooLong.
	p, mc := providerForMode(t, TwoStageBoth)
	mc.EXPECT().
		Converse(gomock.Any(), gomock.Any()).
		Return(nil, errors.New("ValidationException: prompt is too long: 200000 tokens > 200000 maximum"))

	_, err := p.Classify(context.Background(), providers.Request{UserPrompt: "x"})
	var perr *providers.Error
	if !errors.As(err, &perr) {
		t.Fatalf("got %v, want *providers.Error", err)
	}
	if perr.Reason != providers.UnavailableTooLong {
		t.Errorf("Reason = %q", perr.Reason)
	}
}

func TestProvider_TwoStage_TimeoutMaps(t *testing.T) {
	// The provider's per-call timeout fires while the SDK call hangs. classifyErr inspects ctx and returns
	// UnavailableTimeout.
	ctrl := gomock.NewController(t)
	mc := bedrockmocks.NewMockconverser(ctrl)
	mc.EXPECT().
		Converse(gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, _ *bedrockruntime.ConverseInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		})

	p := &Provider{
		cfg:    Config{ModelId: "test-model", TwoStage: TwoStageBoth, Timeout: 5 * time.Millisecond},
		client: mc,
	}
	_, err := p.Classify(context.Background(), providers.Request{UserPrompt: "x"})
	var perr *providers.Error
	if !errors.As(err, &perr) {
		t.Fatalf("got %v, want *providers.Error", err)
	}
	if perr.Reason != providers.UnavailableTimeout {
		t.Errorf("Reason = %q, want UnavailableTimeout", perr.Reason)
	}
}

func TestClassifyDispatch_OffUsesForcedToolUse(t *testing.T) {
	// TwoStageOff must take the Converse-with-tool-config path. We verify the request carried a ToolConfig (only OFF sets
	// it when a schema is provided) — distinguishing it from the XML modes that use Converse without toolConfig.
	ctrl := gomock.NewController(t)
	mc := bedrockmocks.NewMockconverser(ctrl)
	var captured *bedrockruntime.ConverseInput
	mc.EXPECT().
		Converse(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, in *bedrockruntime.ConverseInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error) {
			captured = in
			return outputWithBlocks(toolUseBlock(ToolName, validVerdictDoc())), nil
		})

	p := &Provider{cfg: Config{ModelId: "test-model", TwoStage: TwoStageOff}, client: mc}
	if _, err := p.Classify(context.Background(), providers.Request{
		UserPrompt: "x",
		Schema:     []byte(`{"type":"object"}`),
	}); err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if captured == nil {
		t.Fatal("Converse not called")
	}
	if captured.ToolConfig == nil {
		t.Error("OFF path must set ToolConfig (forced tool use) when Schema is provided")
	}
}

func TestStage1Request_HasStopSequence_BothMode(t *testing.T) {
	// Both mode must wire stop_sequences=["</block>"] on the inference config — that's how Bedrock aborts the generation.
	ctrl := gomock.NewController(t)
	mc := bedrockmocks.NewMockconverser(ctrl)

	var captured *bedrockruntime.ConverseInput
	mc.EXPECT().
		Converse(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, in *bedrockruntime.ConverseInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error) {
			captured = in
			return xmlTextOutput("<block>no</block>"), nil
		})

	p := &Provider{cfg: Config{ModelId: "test", TwoStage: TwoStageBoth, Stage1MaxTokensBoth: 64, Stage1MaxTokensFast: 256}, client: mc}
	if _, err := p.Classify(context.Background(), providers.Request{UserPrompt: "x"}); err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if captured == nil || captured.InferenceConfig == nil {
		t.Fatal("inference config not captured")
	}
	got := captured.InferenceConfig.StopSequences
	if len(got) != 1 || got[0] != stage1StopSequence {
		t.Errorf("StopSequences = %v, want [%q]", got, stage1StopSequence)
	}
	if mt := captured.InferenceConfig.MaxTokens; mt == nil || *mt != 64 {
		t.Errorf("MaxTokens = %v, want 64", mt)
	}
	// OFF mode sets ToolConfig; XML modes must NOT — that's how the model knows to emit XML rather than tool_use blocks.
	if captured.ToolConfig != nil {
		t.Error("two-stage stage 1 must not set ToolConfig (XML format)")
	}
}

func TestStage1Request_NoStopSequence_FastMode(t *testing.T) {
	// Fast mode drops stop sequences and bumps max_tokens so the reason can land in the same response.
	ctrl := gomock.NewController(t)
	mc := bedrockmocks.NewMockconverser(ctrl)

	var captured *bedrockruntime.ConverseInput
	mc.EXPECT().
		Converse(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, in *bedrockruntime.ConverseInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error) {
			captured = in
			return xmlTextOutput("<block>no</block>"), nil
		})

	p := &Provider{cfg: Config{ModelId: "test", TwoStage: TwoStageFast, Stage1MaxTokensBoth: 64, Stage1MaxTokensFast: 256}, client: mc}
	if _, err := p.Classify(context.Background(), providers.Request{UserPrompt: "x"}); err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if captured == nil || captured.InferenceConfig == nil {
		t.Fatal("inference config not captured")
	}
	if got := captured.InferenceConfig.StopSequences; len(got) != 0 {
		t.Errorf("StopSequences = %v, want empty in fast mode", got)
	}
	if mt := captured.InferenceConfig.MaxTokens; mt == nil || *mt != 256 {
		t.Errorf("MaxTokens = %v, want 256", mt)
	}
}

func TestStage1Request_HasCachePoints(t *testing.T) {
	// The system prompt and user content each get a trailing cache_point block so stage 2 (in TwoStageBoth) can hit
	// Bedrock's prompt cache. Verifies wiring; whether the cache actually fires is per-model and out of scope.
	ctrl := gomock.NewController(t)
	mc := bedrockmocks.NewMockconverser(ctrl)

	var captured *bedrockruntime.ConverseInput
	mc.EXPECT().
		Converse(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, in *bedrockruntime.ConverseInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error) {
			captured = in
			return xmlTextOutput("<block>no</block>"), nil
		})

	p := &Provider{cfg: Config{ModelId: "test", TwoStage: TwoStageBoth, Stage1MaxTokensBoth: 64, Stage1MaxTokensFast: 256}, client: mc}
	if _, err := p.Classify(context.Background(), providers.Request{
		SystemPrompt: "sys", UserPrompt: "transcript",
	}); err != nil {
		t.Fatalf("Classify: %v", err)
	}

	if got := len(captured.System); got != 2 {
		t.Fatalf("System blocks = %d, want 2 (text + cache_point)", got)
	}
	sysCP, ok := captured.System[1].(*types.SystemContentBlockMemberCachePoint)
	if !ok {
		t.Fatalf("System[1] type = %T, want CachePoint", captured.System[1])
	}
	if sysCP.Value.Ttl != types.CacheTTLOneHour {
		t.Errorf("System cache point Ttl = %q, want %q", sysCP.Value.Ttl, types.CacheTTLOneHour)
	}
	if got := len(captured.Messages[0].Content); got != 2 {
		t.Fatalf("user blocks = %d, want 2 (text + cache_point)", got)
	}
	userCP, ok := captured.Messages[0].Content[1].(*types.ContentBlockMemberCachePoint)
	if !ok {
		t.Fatalf("Content[1] type = %T, want CachePoint", captured.Messages[0].Content[1])
	}
	if userCP.Value.Ttl != types.CacheTTLOneHour {
		t.Errorf("user cache point Ttl = %q, want %q", userCP.Value.Ttl, types.CacheTTLOneHour)
	}
}

func TestStage2Request_IsXML_NotForcedToolUse(t *testing.T) {
	// On stage-1 block, the escalation must be another XML call, not forced tool use. Sharing the format across stages is
	// what lets stage 2 reuse stage 1's cached prefix.
	ctrl := gomock.NewController(t)
	mc := bedrockmocks.NewMockconverser(ctrl)

	var captures []*bedrockruntime.ConverseInput
	mc.EXPECT().
		Converse(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, in *bedrockruntime.ConverseInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error) {
			captures = append(captures, in)
			if len(captures) == 1 {
				return xmlTextOutput("<block>yes</block>"), nil
			}
			return xmlTextOutput("<block>yes</block><reason>x</reason><tier>soft_deny</tier>"), nil
		}).
		Times(2)

	p := &Provider{cfg: Config{ModelId: "test", TwoStage: TwoStageBoth, Stage1MaxTokensBoth: 64, Stage1MaxTokensFast: 256}, client: mc}
	if _, err := p.Classify(context.Background(), providers.Request{
		SystemPrompt: "sys", UserPrompt: "transcript",
	}); err != nil {
		t.Fatalf("Classify: %v", err)
	}

	if len(captures) != 2 {
		t.Fatalf("got %d Converse calls, want 2", len(captures))
	}
	stage2 := captures[1]
	if stage2.ToolConfig != nil {
		t.Error("stage 2 must not set ToolConfig (XML format)")
	}
	// Stage 2 must drop the stop sequence so the model can emit the full <thinking>...<block>...<reason>...<tier>...
	// payload.
	if got := stage2.InferenceConfig.StopSequences; len(got) != 0 {
		t.Errorf("stage 2 StopSequences = %v, want empty", got)
	}
}

func TestExtractConverseText(t *testing.T) {
	t.Run("nil output", func(t *testing.T) {
		if got := extractConverseText(nil); got != "" {
			t.Errorf("got %q, want empty", got)
		}
	})
	t.Run("concatenates text blocks", func(t *testing.T) {
		out := &bedrockruntime.ConverseOutput{
			Output: &types.ConverseOutputMemberMessage{
				Value: types.Message{
					Content: []types.ContentBlock{
						&types.ContentBlockMemberText{Value: "<block>"},
						&types.ContentBlockMemberText{Value: "yes"},
						&types.ContentBlockMemberText{Value: "</block>"},
					},
				},
			},
		}
		if got := extractConverseText(out); got != "<block>yes</block>" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("ignores tool_use", func(t *testing.T) {
		out := outputWithBlocks(
			&types.ContentBlockMemberText{Value: "hi"},
			toolUseBlock(ToolName, validVerdictDoc()),
		)
		if got := extractConverseText(out); got != "hi" {
			t.Errorf("got %q", got)
		}
	})
}

// captureSingleStage runs one Classify in OFF mode with the given LatencyOptimized setting and returns the
// ConverseInput the provider built. Reduces boilerplate in the request-shape tests below — three different things to
// assert on the same request.
func captureSingleStage(t *testing.T, latencyOptimized bool) *bedrockruntime.ConverseInput {
	t.Helper()
	ctrl := gomock.NewController(t)
	mc := bedrockmocks.NewMockconverser(ctrl)

	var captured *bedrockruntime.ConverseInput
	mc.EXPECT().
		Converse(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, in *bedrockruntime.ConverseInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error) {
			captured = in
			return outputWithBlocks(toolUseBlock(ToolName, validVerdictDoc())), nil
		})

	p := &Provider{
		cfg:    Config{ModelId: "test", TwoStage: TwoStageOff, LatencyOptimized: latencyOptimized},
		client: mc,
	}
	if _, err := p.Classify(context.Background(), providers.Request{
		SystemPrompt: "sys", UserPrompt: "transcript",
	}); err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if captured == nil {
		t.Fatal("Converse was not called")
	}
	return captured
}

func TestSingleStageRequest_HasCachePoints(t *testing.T) {
	// OFF mode (single-stage forced tool use) must emit cache_point markers on both system and user content. Without them,
	// every PreToolUse / PermissionRequest call paid the full prefix cost even though the system prompt + auto-mode policy
	// are identical across calls in a session.
	captured := captureSingleStage(t, false)

	if got := len(captured.System); got != 2 {
		t.Fatalf("System blocks = %d, want 2 (text + cache_point)", got)
	}
	sysCP, ok := captured.System[1].(*types.SystemContentBlockMemberCachePoint)
	if !ok {
		t.Fatalf("System[1] type = %T, want CachePoint", captured.System[1])
	}
	if sysCP.Value.Ttl != types.CacheTTLOneHour {
		t.Errorf("System cache point Ttl = %q, want %q", sysCP.Value.Ttl, types.CacheTTLOneHour)
	}
	if got := len(captured.Messages[0].Content); got != 2 {
		t.Fatalf("user blocks = %d, want 2 (text + cache_point)", got)
	}
	userCP, ok := captured.Messages[0].Content[1].(*types.ContentBlockMemberCachePoint)
	if !ok {
		t.Fatalf("Content[1] type = %T, want CachePoint", captured.Messages[0].Content[1])
	}
	if userCP.Value.Ttl != types.CacheTTLOneHour {
		t.Errorf("user cache point Ttl = %q, want %q", userCP.Value.Ttl, types.CacheTTLOneHour)
	}
}

func TestSingleStageRequest_HasTemperatureZero(t *testing.T) {
	// Verdict semantics depend on a deterministic classifier — same input → same output. Without temperature=0 the
	// orchestrator cache flaps (same prompt, different responses, different hashes) and the verdict can flip across
	// re-asks.
	captured := captureSingleStage(t, false)
	if captured.InferenceConfig == nil {
		t.Fatal("InferenceConfig is nil")
	}
	temp := captured.InferenceConfig.Temperature
	if temp == nil {
		t.Fatal("Temperature is nil; expected 0")
	}
	if *temp != 0 {
		t.Errorf("Temperature = %v, want 0", *temp)
	}
}

func TestSingleStageRequest_PerformanceConfig(t *testing.T) {
	// Default off: PerformanceConfig must be nil so we don't quietly route every classification through optimized
	// inference (which has different pricing on some models).
	t.Run("default off", func(t *testing.T) {
		captured := captureSingleStage(t, false)
		if captured.PerformanceConfig != nil {
			t.Errorf("expected nil PerformanceConfig by default, got %+v", captured.PerformanceConfig)
		}
	})
	// Opt in: optimized.
	t.Run("opt-in optimized", func(t *testing.T) {
		captured := captureSingleStage(t, true)
		if captured.PerformanceConfig == nil {
			t.Fatal("expected PerformanceConfig, got nil")
		}
		if got := captured.PerformanceConfig.Latency; got != types.PerformanceConfigLatencyOptimized {
			t.Errorf("Latency = %q, want %q", got, types.PerformanceConfigLatencyOptimized)
		}
	})
}

func TestStage1Request_LatencyOptimizedPropagates(t *testing.T) {
	// The two-stage path should also honor LatencyOptimized — Bedrock's optimized tier matters most on Haiku, which is
	// exactly the kind of fast model two-stage targets.
	ctrl := gomock.NewController(t)
	mc := bedrockmocks.NewMockconverser(ctrl)

	var captured *bedrockruntime.ConverseInput
	mc.EXPECT().
		Converse(gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, in *bedrockruntime.ConverseInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error) {
			captured = in
			return xmlTextOutput("<block>no</block>"), nil
		})

	p := &Provider{
		cfg: Config{
			ModelId:          "test",
			TwoStage:         TwoStageBoth,
			LatencyOptimized: true,
		},
		client: mc,
	}
	if _, err := p.Classify(context.Background(), providers.Request{UserPrompt: "x"}); err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if captured == nil || captured.PerformanceConfig == nil {
		t.Fatal("PerformanceConfig not propagated to stage 1")
	}
	if got := captured.PerformanceConfig.Latency; got != types.PerformanceConfigLatencyOptimized {
		t.Errorf("Latency = %q, want %q", got, types.PerformanceConfigLatencyOptimized)
	}
}
