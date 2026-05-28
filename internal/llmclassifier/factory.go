package llmclassifier

import (
	"context"
	"errors"
	"fmt"
	"time"

	"claude-auto-permission/internal/config/loader"
	configpb "claude-auto-permission/internal/gen/config/v1"
	"claude-auto-permission/internal/llmclassifier/providers"
	"claude-auto-permission/internal/llmclassifier/providers/bedrock"
)

// ProviderFromConfig dispatches `cfg` to the matching provider's `FromProto`. It's the default value for
// [Decider.provFac] — tests substitute a fixture via [Decider.WithProviderFactory].
//
// Adding a new provider is a one-line dispatch case here plus a `FromProto` in the new sub-package.
//
// On error, returns a nil interface (not a typed-nil concrete pointer) so callers can do `if p == nil` cleanly.
func ProviderFromConfig(ctx context.Context, cfg *configpb.LlmClassifierConfig) (providers.Provider, error) {
	if cfg == nil {
		return nil, errors.New("classifier: nil config")
	}
	// Fill schema defaults before the provider validates, so the default applies on every path into this factory
	// rather than only when an earlier pipeline phase happened to fill first.
	_ = loader.FillDefaults(cfg)
	switch {
	case cfg.GetBedrock() != nil:
		p, err := bedrock.FromProto(ctx, cfg)
		if err != nil {
			return nil, err
		}
		return p, nil
	default:
		return nil, fmt.Errorf("classifier: no provider configured")
	}
}

// ConfigFromProto translates [configpb.LlmClassifierConfig] into the decider's proto-agnostic [Config]. Schema-declared
// defaults from `(opts).default` annotations are applied to fields the caller left unset.
func ConfigFromProto(cfg *configpb.LlmClassifierConfig) Config {
	if cfg == nil {
		return Config{}
	}
	_ = loader.FillDefaults(cfg)
	return Config{
		Enabled:              cfg.GetEnabled(),
		Timeout:              time.Duration(cfg.GetTimeoutMs()) * time.Millisecond,
		MaxConsecutiveBlocks: int(cfg.GetMaxConsecutiveBlocks()),
		MaxSessionBlocks:     int(cfg.GetMaxSessionBlocks()),
		BackstopTtl:          time.Duration(cfg.GetBackstopTtlSeconds()) * time.Second,
		AutoModePolicyTtl:    time.Duration(cfg.GetAutomodePolicyTtlSeconds()) * time.Second,
		Mode:                 modeFromProto(cfg.GetMode()),
		OnClassifierError:    onClassifierErrorFromProto(cfg.GetOnClassifierError()),
	}
}

// modeFromProto maps the proto enum to the typed alias. `UNSPECIFIED` maps to `FullAuto` defensively — FillDefaults
// should have remapped it before this is called, but the mapping stays explicit so a stray zero value doesn't silently
// flip the classifier into block-only.
func modeFromProto(m configpb.LlmClassifierConfig_ClassifierMode) Mode {
	switch m {
	case configpb.LlmClassifierConfig_CLASSIFIER_MODE_BLOCK_ONLY:
		return ModeBlockOnly
	}
	return ModeFullAuto
}

// onClassifierErrorFromProto maps the proto enum to the typed alias. `UNSPECIFIED` maps to `Passthrough` defensively —
// FillDefaults should have remapped it before this is called, but the mapping stays explicit so a stray zero value
// doesn't silently misroute.
func onClassifierErrorFromProto(m configpb.LlmClassifierConfig_OnClassifierError) OnClassifierError {
	switch m {
	case configpb.LlmClassifierConfig_ON_CLASSIFIER_ERROR_ASK:
		return OnClassifierErrorAsk
	}
	return OnClassifierErrorPassthrough
}
