package bedrock

import (
	"context"
	"errors"
	"fmt"
	"time"

	"buf.build/go/protovalidate"

	configpb "claude-auto-permission/internal/gen/config/v1"
)

// FromProto translates a [configpb.LlmClassifierConfig] into a live Bedrock [Provider]. Field-level invariants declared
// on the proto via `(buf.validate)` are enforced here so each provider owns its config validation surface.
//
// Takes the *outer* classifier config (not the inner `BedrockProvider`) so the timeout — declared at the outer level —
// can be threaded in without callers having to reach across the proto boundary themselves.
func FromProto(ctx context.Context, cfg *configpb.LlmClassifierConfig) (*Provider, error) {
	if cfg == nil {
		return nil, errors.New("bedrock: nil config")
	}
	if err := protovalidate.Validate(cfg); err != nil {
		return nil, fmt.Errorf("bedrock: %w", err)
	}
	bp := cfg.GetBedrock()
	return New(ctx, Config{
		ModelId:             bp.GetModelId(),
		MaxTokens:           bp.GetMaxTokens(),
		Timeout:             time.Duration(cfg.GetTimeoutMs()) * time.Millisecond,
		TwoStage:            twoStageMode(bp.GetTwoStage()),
		Stage1MaxTokensBoth: cfg.GetStage1MaxTokensBoth(),
		Stage1MaxTokensFast: cfg.GetStage1MaxTokensFast(),
		LatencyOptimized:    bp.GetLatencyOptimized(),
	})
}

// twoStageMode maps the proto enum to [TwoStageMode]. The enum numbers are kept in sync with the proto schema so
// callers can treat the cast as a simple type punning. `loader.FillDefaults` applies the schema's `(opts).default`
// annotation, so by the time this runs `TWO_STAGE_UNSPECIFIED` should already have been mapped to `TWO_STAGE_BOTH`; the
// explicit case is defense in depth.
func twoStageMode(m configpb.BedrockProvider_TwoStageMode) TwoStageMode {
	switch m {
	case configpb.BedrockProvider_TWO_STAGE_OFF:
		return TwoStageOff
	case configpb.BedrockProvider_TWO_STAGE_FAST:
		return TwoStageFast
	case configpb.BedrockProvider_TWO_STAGE_BOTH:
		return TwoStageBoth
	case configpb.BedrockProvider_TWO_STAGE_THINKING:
		return TwoStageThinking
	}
	return TwoStageBoth
}
