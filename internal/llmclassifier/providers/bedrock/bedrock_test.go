package bedrock

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	bedrockDoc "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"go.uber.org/mock/gomock"

	"claude-auto-permission/internal/llmclassifier/providers"
	bedrockmocks "claude-auto-permission/internal/llmclassifier/providers/bedrock/mocks"
)

func TestNew_RequiresModelID(t *testing.T) {
	_, err := New(context.Background(), Config{})
	if err == nil {
		t.Error("expected error when ModelID is empty")
	}
}

func TestIsPromptTooLong(t *testing.T) {
	tests := []struct {
		s    string
		want bool
	}{
		{"Input is too long for the model", true},
		{"INPUT IS TOO LONG", true},
		{"too many tokens in request", true},
		{"prompt is too long: 200000 > 100000", true},
		{"input exceeds the model's context window", true},
		{"connection refused", false},
		{"throttled", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.s, func(t *testing.T) {
			if got := isPromptTooLong(tt.s); got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// Live-AWS scenarios live in the test/e2e harness, which drives the real binary with a textproto config rather than
// poking the provider in isolation. Run them via `make e2e` with CLAUDE_AUTO_PERMISSION_E2E=1.

// toolUseBlock builds a content block carrying a tool_use with the given name (nil pointer if name is empty) and input
// document.
func toolUseBlock(name string, input bedrockDoc.Interface) types.ContentBlock {
	var namePtr *string
	if name != "" {
		namePtr = aws.String(name)
	}
	return &types.ContentBlockMemberToolUse{
		Value: types.ToolUseBlock{
			ToolUseId: aws.String("tooluse_test"),
			Name:      namePtr,
			Input:     input,
		},
	}
}

// outputWithBlocks wraps a slice of content blocks in the ConverseOutput shape Bedrock would return for a normal
// response.
func outputWithBlocks(blocks ...types.ContentBlock) *bedrockruntime.ConverseOutput {
	return &bedrockruntime.ConverseOutput{
		Output: &types.ConverseOutputMemberMessage{
			Value: types.Message{
				Role:    types.ConversationRoleAssistant,
				Content: blocks,
			},
		},
	}
}

// validVerdictDoc returns a document carrying a well-formed classifier verdict, matching the wire shape we observed
// against real Bedrock.
func validVerdictDoc() bedrockDoc.Interface {
	return bedrockDoc.NewLazyDocument(map[string]any{
		"thinking":    "user asked to delete one file but agent is doing rm -rf",
		"shouldBlock": true,
		"reason":      "rm -rf is destructive",
	})
}

func TestParseConverseOutput(t *testing.T) {
	tests := []struct {
		name        string
		out         *bedrockruntime.ConverseOutput
		wantErrSub  string // substring expected in the returned error
		wantBlock   bool
		wantReason  string
		wantRawJSON bool
	}{
		{
			name:       "nil output",
			out:        nil,
			wantErrSub: "empty Converse output",
		},
		{
			name:       "nil Output field",
			out:        &bedrockruntime.ConverseOutput{Output: nil},
			wantErrSub: "empty Converse output",
		},
		{
			name: "unknown union member",
			out: &bedrockruntime.ConverseOutput{
				Output: &types.UnknownUnionMember{Tag: "future_field"},
			},
			wantErrSub: "unexpected output type",
		},
		{
			name:       "empty content",
			out:        outputWithBlocks(),
			wantErrSub: "no " + ToolName + " tool_use block",
		},
		{
			name:       "text-only response (real shape when model declines tool)",
			out:        outputWithBlocks(&types.ContentBlockMemberText{Value: "Hello!"}),
			wantErrSub: "no " + ToolName + " tool_use block",
		},
		{
			name:       "tool name does not match",
			out:        outputWithBlocks(toolUseBlock("other_tool", validVerdictDoc())),
			wantErrSub: "no " + ToolName + " tool_use block",
		},
		{
			name:       "nil tool name",
			out:        outputWithBlocks(toolUseBlock("", validVerdictDoc())),
			wantErrSub: "no " + ToolName + " tool_use block",
		},
		{
			name: "input document fails to marshal",
			out: outputWithBlocks(toolUseBlock(ToolName,
				// time.Time triggers smithy's InvalidMarshalError, surfacing through our marshalDocument path.
				bedrockDoc.NewLazyDocument(struct{ T time.Time }{T: time.Now()}),
			)),
			wantErrSub: "marshal tool input",
		},
		{
			name: "verdict decode fails (type mismatch)",
			out: outputWithBlocks(toolUseBlock(ToolName,
				bedrockDoc.NewLazyDocument(map[string]any{
					"shouldBlock": "not a bool",
				}),
			)),
			wantErrSub: "decode verdict",
		},
		{
			name:        "happy path",
			out:         outputWithBlocks(toolUseBlock(ToolName, validVerdictDoc())),
			wantBlock:   true,
			wantReason:  "rm -rf is destructive",
			wantRawJSON: true,
		},
		{
			name: "multi-block: text + non-matching tool + matching tool",
			out: outputWithBlocks(
				&types.ContentBlockMemberText{Value: "Thinking..."},
				toolUseBlock("other_tool", validVerdictDoc()),
				toolUseBlock(ToolName, validVerdictDoc()),
			),
			wantBlock:   true,
			wantReason:  "rm -rf is destructive",
			wantRawJSON: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := parseConverseOutput(tt.out)

			if tt.wantErrSub != "" {
				if err == nil {
					t.Fatalf("got nil error, want one containing %q", tt.wantErrSub)
				}
				if !strings.Contains(err.Error(), tt.wantErrSub) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.wantErrSub)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.ShouldBlock != tt.wantBlock {
				t.Errorf("ShouldBlock = %v, want %v", res.ShouldBlock, tt.wantBlock)
			}
			if res.Reason != tt.wantReason {
				t.Errorf("Reason = %q, want %q", res.Reason, tt.wantReason)
			}
			if tt.wantRawJSON {
				if len(res.RawResponse) == 0 {
					t.Error("RawResponse is empty")
				}
				// Ensure RawResponse is valid JSON describing the verdict.
				var probe map[string]any
				if uerr := json.Unmarshal(res.RawResponse, &probe); uerr != nil {
					t.Errorf("RawResponse is not valid JSON: %v", uerr)
				}
			}
		})
	}
}

func TestBuildToolConfig(t *testing.T) {
	t.Run("invalid JSON", func(t *testing.T) {
		_, err := buildToolConfig([]byte("not json"))
		if err == nil || !strings.Contains(err.Error(), "decode schema") {
			t.Fatalf("got %v, want error containing 'decode schema'", err)
		}
	})

	t.Run("nil bytes", func(t *testing.T) {
		_, err := buildToolConfig(nil)
		if err == nil || !strings.Contains(err.Error(), "decode schema") {
			t.Fatalf("got %v, want error containing 'decode schema'", err)
		}
	})

	t.Run("valid minimal schema", func(t *testing.T) {
		cfg, err := buildToolConfig([]byte(`{"type":"object"}`))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg == nil {
			t.Fatal("nil ToolConfiguration")
		}
		// Two entries: the tool spec, then a trailing cache point. The cache point lets Bedrock's prompt cache reuse the
		// (constant) tool definition across calls.
		if len(cfg.Tools) != 2 {
			t.Fatalf("len(Tools) = %d, want 2 (spec + cache_point)", len(cfg.Tools))
		}
		spec, ok := cfg.Tools[0].(*types.ToolMemberToolSpec)
		if !ok {
			t.Fatalf("Tool[0] type = %T, want *ToolMemberToolSpec", cfg.Tools[0])
		}
		if spec.Value.Name == nil || *spec.Value.Name != ToolName {
			t.Errorf("tool name = %v, want %q", spec.Value.Name, ToolName)
		}
		cp, ok := cfg.Tools[1].(*types.ToolMemberCachePoint)
		if !ok {
			t.Fatalf("Tool[1] type = %T, want *ToolMemberCachePoint", cfg.Tools[1])
		}
		if cp.Value.Ttl != types.CacheTTLOneHour {
			t.Errorf("tool cache point Ttl = %q, want %q", cp.Value.Ttl, types.CacheTTLOneHour)
		}
		choice, ok := cfg.ToolChoice.(*types.ToolChoiceMemberTool)
		if !ok {
			t.Fatalf("ToolChoice type = %T, want *ToolChoiceMemberTool", cfg.ToolChoice)
		}
		if choice.Value.Name == nil || *choice.Value.Name != ToolName {
			t.Errorf("toolChoice name = %v, want %q", choice.Value.Name, ToolName)
		}
	})
}

// classifyHappyPathBlocks is the fixture our mock returns for the happy-path Classify test. Mirrors the shape we
// observed against real Bedrock in plan-mode probing.
func classifyHappyPathBlocks() *bedrockruntime.ConverseOutput {
	return outputWithBlocks(toolUseBlock(ToolName, validVerdictDoc()))
}

func TestProvider_Classify_HappyPath(t *testing.T) {
	ctrl := gomock.NewController(t)
	mc := bedrockmocks.NewMockconverser(ctrl)

	mc.EXPECT().
		Converse(gomock.Any(), gomock.Any()).
		Return(classifyHappyPathBlocks(), nil)

	p := &Provider{
		cfg:    Config{ModelId: "test-model", TwoStage: TwoStageOff},
		client: mc,
	}

	res, err := p.Classify(context.Background(), providers.Request{
		UserPrompt: "anything",
	})
	if err != nil {
		t.Fatalf("Classify: %v", err)
	}
	if !res.ShouldBlock {
		t.Error("ShouldBlock = false, want true")
	}
	if res.Reason != "rm -rf is destructive" {
		t.Errorf("Reason = %q", res.Reason)
	}
	if len(res.RawResponse) == 0 {
		t.Error("RawResponse empty")
	}
	if res.LatencyMs < 0 {
		t.Errorf("LatencyMs = %d", res.LatencyMs)
	}
}

func TestProvider_Classify_ErrorRouting(t *testing.T) {
	tests := []struct {
		name string
		// setup configures the mock and returns a Provider + Request to drive Classify with.
		setup      func(t *testing.T, ctrl *gomock.Controller) (*Provider, providers.Request)
		wantReason providers.UnavailableReason
	}{
		{
			name: "context deadline maps to UnavailableTimeout",
			setup: func(t *testing.T, ctrl *gomock.Controller) (*Provider, providers.Request) {
				mc := bedrockmocks.NewMockconverser(ctrl)
				// Block until the context Provider sets up actually expires. Provider.Classify wraps with cfg.Timeout, so we wait
				// on the inbound ctx and surface DeadlineExceeded back through the SDK-style return shape.
				mc.EXPECT().
					Converse(gomock.Any(), gomock.Any()).
					DoAndReturn(func(ctx context.Context, _ *bedrockruntime.ConverseInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error) {
						<-ctx.Done()
						return nil, ctx.Err()
					})
				p := &Provider{
					cfg:    Config{ModelId: "test", TwoStage: TwoStageOff, Timeout: 5 * time.Millisecond},
					client: mc,
				}
				return p, providers.Request{UserPrompt: "anything"}
			},
			wantReason: providers.UnavailableTimeout,
		},
		{
			name: "Bedrock prompt-too-long maps to UnavailableTooLong",
			setup: func(t *testing.T, ctrl *gomock.Controller) (*Provider, providers.Request) {
				mc := bedrockmocks.NewMockconverser(ctrl)
				// Phrasing copied verbatim from a real Bedrock ValidationException.
				mc.EXPECT().
					Converse(gomock.Any(), gomock.Any()).
					Return(nil, errors.New("ValidationException: prompt is too long: 200076 tokens > 200000 maximum"))
				p := &Provider{cfg: Config{ModelId: "test", TwoStage: TwoStageOff}, client: mc}
				return p, providers.Request{UserPrompt: "anything"}
			},
			wantReason: providers.UnavailableTooLong,
		},
		{
			name: "generic SDK error maps to UnavailableProviderErr",
			setup: func(t *testing.T, ctrl *gomock.Controller) (*Provider, providers.Request) {
				mc := bedrockmocks.NewMockconverser(ctrl)
				mc.EXPECT().
					Converse(gomock.Any(), gomock.Any()).
					Return(nil, errors.New("ThrottlingException: Rate exceeded"))
				p := &Provider{cfg: Config{ModelId: "test", TwoStage: TwoStageOff}, client: mc}
				return p, providers.Request{UserPrompt: "anything"}
			},
			wantReason: providers.UnavailableProviderErr,
		},
		{
			name: "buildToolConfig failure maps to UnavailableProviderErr without calling Converse",
			setup: func(t *testing.T, ctrl *gomock.Controller) (*Provider, providers.Request) {
				// No EXPECT() — Converse must not be called when schema parsing fails.
				mc := bedrockmocks.NewMockconverser(ctrl)
				p := &Provider{cfg: Config{ModelId: "test", TwoStage: TwoStageOff}, client: mc}
				return p, providers.Request{
					UserPrompt: "anything",
					Schema:     json.RawMessage("not json"),
				}
			},
			wantReason: providers.UnavailableProviderErr,
		},
		{
			name: "parse failure on unexpected output type maps to UnavailableParse",
			setup: func(t *testing.T, ctrl *gomock.Controller) (*Provider, providers.Request) {
				mc := bedrockmocks.NewMockconverser(ctrl)
				mc.EXPECT().
					Converse(gomock.Any(), gomock.Any()).
					Return(&bedrockruntime.ConverseOutput{Output: &types.UnknownUnionMember{Tag: "future"}}, nil)
				p := &Provider{cfg: Config{ModelId: "test", TwoStage: TwoStageOff}, client: mc}
				return p, providers.Request{UserPrompt: "anything"}
			},
			wantReason: providers.UnavailableParse,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			p, req := tt.setup(t, ctrl)

			res, err := p.Classify(context.Background(), req)

			var perr *providers.Error
			if !errors.As(err, &perr) {
				t.Fatalf("got err %v, want *providers.Error", err)
			}
			if perr.Reason != tt.wantReason {
				t.Errorf("Reason = %q, want %q", perr.Reason, tt.wantReason)
			}
			if res.LatencyMs < 0 {
				t.Errorf("LatencyMs = %d (want >= 0)", res.LatencyMs)
			}
		})
	}
}
