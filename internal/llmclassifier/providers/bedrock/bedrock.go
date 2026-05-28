// Package bedrock implements the classifier provider against Bedrock's Converse API.
//
// TwoStageOff: forced tool use — declare the classify_result tool with the verdict schema, force toolChoice, parse the
// resulting tool_use block. Schema enforced at the API layer.
//
// TwoStageBoth/Fast: stage 1 is a quick XML call with max_tokens and `</block>` stop_sequences asking for
// `<block>yes/no</block>`. Allow verdicts return immediately. In Both, block verdicts escalate to a chain-of-thought
// stage 2 for the authoritative reason; in Fast, stage 1's reason is final. The shared prefix carries a cache_point so
// stage 2 reuses Bedrock's prompt cache.
//
// TwoStageThinking: single non-streaming XML chain-of-thought call.
package bedrock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsConfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	bedrockDoc "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"claude-auto-permission/internal/llmclassifier/providers"
)

// ToolName matches the system prompt's `classify_result` reference. Extracted from the model's tool_use block as the
// verdict.
const ToolName = "classify_result"

// TwoStageMode picks single- vs two-stage classification. Values match BedrockProvider.TwoStageMode in the proto so
// callers can translate by simple cast. The proto-zero `TwoStageUnspecified` is a sentinel; the production default
// (`TwoStageBoth`) is applied via the schema's `(opts).default` annotation in `loader.FillDefaults`.
type TwoStageMode int

const (
	// TwoStageUnspecified is the proto-zero sentinel. Treated as `TwoStageBoth` by [Provider.Classify] for defense in
	// depth, but the schema default should remap it before this code sees it.
	TwoStageUnspecified TwoStageMode = iota
	// TwoStageOff: single Converse with forced tool use. Haiku 4.5 has a known issue dropping shouldBlock from structured
	// output.
	TwoStageOff
	// TwoStageFast: stage 1 only. Verdict and reason both from the XML; no stop_sequences, larger max_tokens. Lower
	// latency on blocks at the cost of higher false-positive rate.
	TwoStageFast
	// TwoStageBoth: production default. Stage 1 XML with stop_sequences `</block>`; on block, stage 2 chain-of-thought for
	// the authoritative reason.
	TwoStageBoth
	// TwoStageThinking: single non-streaming XML chain-of-thought call. A/B baseline.
	TwoStageThinking
)

// Config holds the per-invocation knobs. AWS region and credentials resolve through the SDK's default chain; to
// override per-classifier, prefix the hook command with env vars (AWS_PROFILE=…).
type Config struct {
	// ModelId is the Bedrock model identifier or cross-region inference profile ID. Required.
	ModelId string

	// MaxTokens caps tool-use output. Default 1024.
	MaxTokens int32

	// Timeout bounds the whole round-trip (both stages in two-stage modes).
	Timeout time.Duration

	// TwoStage selects single- vs two-stage classification.
	TwoStage TwoStageMode

	// Stage-1 max_tokens budgets.
	Stage1MaxTokensBoth int32
	Stage1MaxTokensFast int32

	// LatencyOptimized opts into Bedrock's latency-optimized inference tier. Has no effect on unsupported models. Pricing
	// differs on some models — opt in deliberately.
	LatencyOptimized bool
}

// Provider is the Bedrock-native classifier implementation.
type Provider struct {
	cfg    Config
	client converser
}

// converser is the slice of *bedrockruntime.Client we use; extracted as an interface so tests can swap in a mock. The
// real *bedrockruntime.Client satisfies it.
//
// All paths use non-streaming Converse — stop_sequences aborts generation regardless of transport, and non-streaming
// avoids event-stream connection overhead.
//
//go:generate go run go.uber.org/mock/mockgen@v0.6.0 -typed -source=bedrock.go -destination=mocks/converser_mock.go -package=mocks
type converser interface {
	Converse(ctx context.Context, params *bedrockruntime.ConverseInput, optFns ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error)
}

// New constructs a Provider, loading AWS config eagerly so credential resolution / SSO refresh costs are paid once per
// process.
func New(ctx context.Context, cfg Config) (*Provider, error) {
	if cfg.ModelId == "" {
		return nil, errors.New("bedrock: ModelId is required")
	}

	awsCfg, err := awsConfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("bedrock: load AWS config: %w", err)
	}

	return &Provider{
		cfg:    cfg,
		client: bedrockruntime.NewFromConfig(awsCfg),
	}, nil
}

func (p *Provider) Name() string { return "bedrock" }

func (p *Provider) Model() string { return p.cfg.ModelId }

// Classify dispatches to the single- or two-stage path per cfg.TwoStage. `TwoStageUnspecified` (the proto zero) routes
// to the production default `TwoStageBoth` defensively — `loader.FillDefaults` should remap it before the provider sees
// it, but the fallback ensures a sane verdict if a caller bypasses the loader.
func (p *Provider) Classify(ctx context.Context, req providers.Request) (providers.Result, error) {
	if p.cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.cfg.Timeout)
		defer cancel()
	}

	switch p.cfg.TwoStage {
	case TwoStageOff:
		return p.classifyForcedToolUse(ctx, req)
	case TwoStageFast, TwoStageBoth, TwoStageUnspecified:
		return p.classifyTwoStage(ctx, req)
	case TwoStageThinking:
		return p.classifyXml(ctx, req, xmlS2Suffix, p.maxTokens(), nil)
	default:
		return p.classifyTwoStage(ctx, req)
	}
}

// classifyForcedToolUse: one Converse call with toolConfig forcing the model to emit a classify_result tool_use block.
// System prompt and user content carry trailing cache_point blocks so subsequent calls reuse Bedrock's prompt cache.
// Temperature is pinned to 0 — sampling diversity has no upside on a yes/no verdict.
func (p *Provider) classifyForcedToolUse(ctx context.Context, req providers.Request) (providers.Result, error) {
	input := &bedrockruntime.ConverseInput{
		ModelId:           aws.String(p.cfg.ModelId),
		Messages:          userMessages(req.UserPrefix, req.UserPrompt),
		InferenceConfig:   p.inferenceConfig(p.maxTokens(), nil),
		PerformanceConfig: p.performanceConfig(),
	}

	if req.SystemPrompt != "" {
		input.System = cachedSystemBlocks(req.SystemPrompt)
	}

	if len(req.Schema) > 0 {
		toolConfig, err := buildToolConfig(req.Schema)
		if err != nil {
			return providers.Result{}, &providers.Error{
				Reason: providers.UnavailableProviderErr,
				Err:    fmt.Errorf("build tool config: %w", err),
			}
		}
		input.ToolConfig = toolConfig
	}

	start := time.Now()
	out, err := p.client.Converse(ctx, input)
	latency := int(time.Since(start) / time.Millisecond)

	if err != nil {
		return providers.Result{LatencyMs: latency}, classifyErr(ctx, err, latency)
	}

	res, parseErr := parseConverseOutput(out)
	if parseErr != nil {
		// Pass through tagged errors; wrap raw ones.
		if perr, ok := errors.AsType[*providers.Error](parseErr); ok {
			perr.Err = fmt.Errorf("%w (latency=%dms)", perr.Err, latency)
			return providers.Result{LatencyMs: latency}, perr
		}
		return providers.Result{LatencyMs: latency}, &providers.Error{
			Reason: providers.UnavailableParse,
			Err:    parseErr,
		}
	}
	res.LatencyMs = latency
	return res, nil
}

func (p *Provider) maxTokens() int32 {
	return p.cfg.MaxTokens
}

// inferenceConfig builds the shared InferenceConfiguration. stopSequences may be nil; callers that need them (stage 1
// BOTH) pass `["</block>"]`.
func (p *Provider) inferenceConfig(maxTokens int32, stopSequences []string) *types.InferenceConfiguration {
	cfg := &types.InferenceConfiguration{
		MaxTokens:   aws.Int32(maxTokens),
		Temperature: aws.Float32(0),
	}
	if len(stopSequences) > 0 {
		cfg.StopSequences = stopSequences
	}
	return cfg
}

// performanceConfig returns nil unless LatencyOptimized is set. Bedrock silently falls back to standard inference on
// models that don't support the optimized tier.
func (p *Provider) performanceConfig() *types.PerformanceConfiguration {
	if !p.cfg.LatencyOptimized {
		return nil
	}
	return &types.PerformanceConfiguration{
		Latency: types.PerformanceConfigLatencyOptimized,
	}
}

// classifyErr maps an SDK error to a tagged providers.Error so the orchestrator can distinguish timeout,
// prompt-too-long, and generic outage. Shared between the single-stage and two-stage paths.
func classifyErr(ctx context.Context, err error, latency int) error {
	// Distinguish timeout (context deadline) from other errors.
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &providers.Error{
			Reason: providers.UnavailableTimeout,
			Err:    fmt.Errorf("after %dms", latency),
		}
	}
	if isPromptTooLong(err.Error()) {
		return &providers.Error{
			Reason: providers.UnavailableTooLong,
			Err:    err,
		}
	}
	return &providers.Error{
		Reason: providers.UnavailableProviderErr,
		Err:    err,
	}
}

// buildToolConfig forces the model to emit one tool_use block satisfying the supplied JSON Schema. A trailing cache
// point lets Bedrock cache the constant tool definition across calls.
func buildToolConfig(schema json.RawMessage) (*types.ToolConfiguration, error) {
	var schemaDoc map[string]any
	if err := json.Unmarshal(schema, &schemaDoc); err != nil {
		return nil, fmt.Errorf("decode schema: %w", err)
	}
	return &types.ToolConfiguration{
		Tools: []types.Tool{
			&types.ToolMemberToolSpec{
				Value: types.ToolSpecification{
					Name:        aws.String(ToolName),
					Description: aws.String("Report the security classification result for the agent action"),
					InputSchema: &types.ToolInputSchemaMemberJson{
						Value: bedrockDoc.NewLazyDocument(schemaDoc),
					},
				},
			},
			&types.ToolMemberCachePoint{
				Value: types.CachePointBlock{Type: types.CachePointTypeDefault, Ttl: cacheTTL},
			},
		},
		ToolChoice: &types.ToolChoiceMemberTool{
			Value: types.SpecificToolChoice{
				Name: aws.String(ToolName),
			},
		},
	}, nil
}

// parseConverseOutput finds the classify_result tool_use block and decodes its input as the verdict.
func parseConverseOutput(out *bedrockruntime.ConverseOutput) (providers.Result, error) {
	if out == nil || out.Output == nil {
		return providers.Result{}, errors.New("empty Converse output")
	}
	msg, ok := out.Output.(*types.ConverseOutputMemberMessage)
	if !ok {
		return providers.Result{}, fmt.Errorf("unexpected output type: %T", out.Output)
	}

	for _, block := range msg.Value.Content {
		toolUse, ok := block.(*types.ContentBlockMemberToolUse)
		if !ok {
			continue
		}
		if toolUse.Value.Name == nil || *toolUse.Value.Name != ToolName {
			continue
		}
		raw, err := marshalDocument(toolUse.Value.Input)
		if err != nil {
			return providers.Result{}, fmt.Errorf("marshal tool input: %w", err)
		}
		var parsed struct {
			ShouldBlock *bool  `json:"shouldBlock"`
			Reason      string `json:"reason"`
		}
		if err := json.Unmarshal(raw, &parsed); err != nil {
			return providers.Result{}, fmt.Errorf("decode verdict: %w", err)
		}
		if parsed.ShouldBlock == nil {
			// Fail closed on malformed output, matching Claude Code's reference impl.
			return providers.Result{
				ShouldBlock: true,
				Reason:      "model omitted shouldBlock — blocking for safety",
				RawResponse: raw,
			}, nil
		}
		return providers.Result{
			ShouldBlock: *parsed.ShouldBlock,
			Reason:      parsed.Reason,
			RawResponse: raw,
		}, nil
	}

	return providers.Result{}, fmt.Errorf("no %s tool_use block in response", ToolName)
}

// marshalDocument re-serializes a Bedrock document. MarshalSmithyDocument works for both response-side
// documentUnmarshalers (real Bedrock) and request-side documentMarshalers (NewLazyDocument from tests); the
// UnmarshalSmithyDocument round-trip rejects the latter.
func marshalDocument(d bedrockDoc.Interface) ([]byte, error) {
	if d == nil {
		return nil, errors.New("nil document")
	}
	return d.MarshalSmithyDocument()
}

// isPromptTooLong recognizes Bedrock's prompt-too-long ValidationException across the variant phrasings the API emits.
func isPromptTooLong(s string) bool {
	lower := strings.ToLower(s)
	return strings.Contains(lower, "input is too long") ||
		strings.Contains(lower, "too many tokens") ||
		strings.Contains(lower, "prompt is too long") ||
		strings.Contains(lower, "exceed") && strings.Contains(lower, "context")
}
