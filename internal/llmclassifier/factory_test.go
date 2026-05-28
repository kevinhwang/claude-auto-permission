package llmclassifier

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"

	configpb "claude-auto-permission/internal/gen/config/v1"
	"claude-auto-permission/internal/llmclassifier/providers/bedrock"
)

// providerCfg returns the unexported cfg field of a provider via reflect+unsafe. We deliberately don't add production
// accessors just to peek at the wired-up Config from the factory test — the trick is scoped to this file and lets us
// verify translation without bloating the provider API.
func providerCfg[T any](t *testing.T, p any) T {
	t.Helper()
	v := reflect.ValueOf(p).Elem().FieldByName("cfg")
	if !v.IsValid() {
		t.Fatalf("provider %T has no cfg field", p)
	}
	return reflect.NewAt(v.Type(), unsafe.Pointer(v.UnsafeAddr())).Elem().Interface().(T)
}

func TestProviderFromConfig(t *testing.T) {
	timeoutMs := int32(5000)
	defaultedBedrock := configpb.BedrockProvider_builder{
		MaxTokens: ptr(int32(2048)),
	}.Build()
	emptyModelBedrock := configpb.BedrockProvider_builder{
		ModelId: ptr(""),
	}.Build()
	fullBedrock := configpb.BedrockProvider_builder{
		ModelId:   ptr("us.anthropic.claude-haiku-4-5-20251001-v1:0"),
		MaxTokens: ptr(int32(2048)),
	}.Build()

	tests := []struct {
		name       string
		cfg        *configpb.LlmClassifierConfig
		wantErr    bool
		wantErrSub string
		wantName   string
		assertCfg  func(t *testing.T, p any)
	}{
		{
			name:       "nil cfg",
			cfg:        nil,
			wantErr:    true,
			wantErrSub: "nil config",
		},
		{
			name: "no provider set",
			cfg: configpb.LlmClassifierConfig_builder{
				Enabled:   ptr(true),
				TimeoutMs: &timeoutMs,
			}.Build(),
			wantErr:    true,
			wantErrSub: "no provider configured",
		},
		{
			name: "bedrock without model_id gets the default",
			cfg: configpb.LlmClassifierConfig_builder{
				Bedrock: defaultedBedrock,
			}.Build(),
			wantName: "bedrock",
			assertCfg: func(t *testing.T, p any) {
				got := providerCfg[bedrock.Config](t, p)
				if got.ModelId != "global.anthropic.claude-sonnet-4-6" {
					t.Errorf("ModelId = %q, want the schema default", got.ModelId)
				}
			},
		},
		{
			name: "bedrock with explicitly empty model_id is rejected",
			cfg: configpb.LlmClassifierConfig_builder{
				Bedrock: emptyModelBedrock,
			}.Build(),
			wantErr:    true,
			wantErrSub: "model_id: must be at least 1 characters",
		},
		{
			name: "bedrock with model_id and timeout",
			// bedrock.New only validates model_id and lazily loads AWS config — no live credentials are required to succeed
			// here, which lets this test run anywhere.
			cfg: configpb.LlmClassifierConfig_builder{
				Bedrock:   fullBedrock,
				TimeoutMs: &timeoutMs,
			}.Build(),
			wantName: "bedrock",
			assertCfg: func(t *testing.T, p any) {
				got := providerCfg[bedrock.Config](t, p)
				// Unset fields take their schema defaults: `two_stage` → TwoStageBoth (the production default), and the
				// stage-1 token budgets → 64/256.
				want := bedrock.Config{
					ModelId:             "us.anthropic.claude-haiku-4-5-20251001-v1:0",
					MaxTokens:           2048,
					Timeout:             5 * time.Second,
					TwoStage:            bedrock.TwoStageBoth,
					Stage1MaxTokensBoth: 64,
					Stage1MaxTokensFast: 256,
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("bedrock.Config = %+v, want %+v", got, want)
				}
			},
		},
		{
			name: "bedrock with two_stage=BOTH",
			cfg: configpb.LlmClassifierConfig_builder{
				Bedrock: configpb.BedrockProvider_builder{
					ModelId:  ptr("us.anthropic.claude-haiku-4-5-20251001-v1:0"),
					TwoStage: ptr(configpb.BedrockProvider_TWO_STAGE_MODE_BOTH),
				}.Build(),
			}.Build(),
			wantName: "bedrock",
			assertCfg: func(t *testing.T, p any) {
				got := providerCfg[bedrock.Config](t, p)
				if got.TwoStage != bedrock.TwoStageBoth {
					t.Errorf("TwoStage = %v, want TwoStageBoth", got.TwoStage)
				}
			},
		},
		{
			name: "bedrock with two_stage=FAST",
			cfg: configpb.LlmClassifierConfig_builder{
				Bedrock: configpb.BedrockProvider_builder{
					ModelId:  ptr("us.anthropic.claude-haiku-4-5-20251001-v1:0"),
					TwoStage: ptr(configpb.BedrockProvider_TWO_STAGE_MODE_FAST),
				}.Build(),
			}.Build(),
			wantName: "bedrock",
			assertCfg: func(t *testing.T, p any) {
				got := providerCfg[bedrock.Config](t, p)
				if got.TwoStage != bedrock.TwoStageFast {
					t.Errorf("TwoStage = %v, want TwoStageFast", got.TwoStage)
				}
			},
		},
		{
			name: "bedrock with two_stage=THINKING",
			cfg: configpb.LlmClassifierConfig_builder{
				Bedrock: configpb.BedrockProvider_builder{
					ModelId:  ptr("us.anthropic.claude-haiku-4-5-20251001-v1:0"),
					TwoStage: ptr(configpb.BedrockProvider_TWO_STAGE_MODE_THINKING),
				}.Build(),
			}.Build(),
			wantName: "bedrock",
			assertCfg: func(t *testing.T, p any) {
				got := providerCfg[bedrock.Config](t, p)
				if got.TwoStage != bedrock.TwoStageThinking {
					t.Errorf("TwoStage = %v, want TwoStageThinking", got.TwoStage)
				}
			},
		},
		{
			name: "bedrock without two_stage defaults to BOTH",
			// Sentinel: leaving the proto field unset (or TWO_STAGE_MODE_BOTH=0) must yield bedrock.TwoStageBoth. Production default
			// — matches Claude Code's auto mode.
			cfg: configpb.LlmClassifierConfig_builder{
				Bedrock: fullBedrock,
			}.Build(),
			wantName: "bedrock",
			assertCfg: func(t *testing.T, p any) {
				got := providerCfg[bedrock.Config](t, p)
				if got.TwoStage != bedrock.TwoStageBoth {
					t.Errorf("TwoStage = %v, want TwoStageBoth", got.TwoStage)
				}
			},
		},
		{
			name: "bedrock with latency_optimized=true",
			cfg: configpb.LlmClassifierConfig_builder{
				Bedrock: configpb.BedrockProvider_builder{
					ModelId:          ptr("us.anthropic.claude-haiku-4-5-20251001-v1:0"),
					LatencyOptimized: ptr(true),
				}.Build(),
			}.Build(),
			wantName: "bedrock",
			assertCfg: func(t *testing.T, p any) {
				got := providerCfg[bedrock.Config](t, p)
				if !got.LatencyOptimized {
					t.Errorf("LatencyOptimized = false, want true")
				}
			},
		},
		{
			name: "bedrock without latency_optimized defaults to false",
			// Sentinel: opt-in only. Routing every classification through the optimized inference tier silently would be
			// surprising on models with different pricing.
			cfg: configpb.LlmClassifierConfig_builder{
				Bedrock: fullBedrock,
			}.Build(),
			wantName: "bedrock",
			assertCfg: func(t *testing.T, p any) {
				got := providerCfg[bedrock.Config](t, p)
				if got.LatencyOptimized {
					t.Errorf("LatencyOptimized = true, want false")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := ProviderFromConfig(context.Background(), tt.cfg)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("got nil error, want error containing %q", tt.wantErrSub)
				}
				if !strings.Contains(err.Error(), tt.wantErrSub) {
					t.Errorf("error = %q, want substring %q", err.Error(), tt.wantErrSub)
				}
				if p != nil {
					t.Errorf("expected nil provider on error, got %T", p)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if p == nil {
				t.Fatal("expected non-nil provider")
			}
			if got := p.Name(); got != tt.wantName {
				t.Errorf("Name() = %q, want %q", got, tt.wantName)
			}
			if tt.assertCfg != nil {
				tt.assertCfg(t, p)
			}
		})
	}
}

func TestConfigFromProto(t *testing.T) {
	tests := []struct {
		name string
		cfg  *configpb.LlmClassifierConfig
		want Config
	}{
		{
			name: "nil cfg yields zero Config",
			cfg:  nil,
			want: Config{},
		},
		{
			name: "all fields translate correctly",
			cfg: configpb.LlmClassifierConfig_builder{
				Enabled:                  ptr(true),
				TimeoutMs:                ptr(int32(8000)),
				MaxConsecutiveBlocks:     ptr(int32(5)),
				MaxSessionBlocks:         ptr(int32(40)),
				BackstopTtlSeconds:       ptr(int32(1800)),
				AutomodePolicyTtlSeconds: ptr(int32(3600)),
				Mode:                     ptr(configpb.LlmClassifierConfig_CLASSIFIER_MODE_BLOCK_ONLY),
			}.Build(),
			want: Config{
				Enabled:              true,
				Timeout:              8 * time.Second,
				MaxConsecutiveBlocks: 5,
				MaxSessionBlocks:     40,
				BackstopTtl:          30 * time.Minute,
				AutoModePolicyTtl:    time.Hour,
				Mode:                 ModeBlockOnly,
			},
		},
		{
			name: "unset fields get schema defaults from (opts).default annotations",
			cfg:  configpb.LlmClassifierConfig_builder{}.Build(),
			want: Config{
				Timeout:              8 * time.Second,
				MaxConsecutiveBlocks: 3,
				MaxSessionBlocks:     20,
				BackstopTtl:          30 * time.Minute,
				AutoModePolicyTtl:    24 * time.Hour,
				Mode:                 ModeFullAuto,
				OnClassifierError:    OnClassifierErrorPassthrough,
			},
		},
		{
			name: "on_classifier_error=ASK translates to OnClassifierErrorAsk",
			cfg: configpb.LlmClassifierConfig_builder{
				OnClassifierError: ptr(configpb.LlmClassifierConfig_ON_CLASSIFIER_ERROR_ASK),
			}.Build(),
			want: Config{
				Timeout:              8 * time.Second,
				MaxConsecutiveBlocks: 3,
				MaxSessionBlocks:     20,
				BackstopTtl:          30 * time.Minute,
				AutoModePolicyTtl:    24 * time.Hour,
				Mode:                 ModeFullAuto,
				OnClassifierError:    OnClassifierErrorAsk,
			},
		},
		{
			// Sentinel: explicit UNSPECIFIED — what FillDefaults would never produce, but a misconfigured caller could — must
			// defensively map to Passthrough rather than silently becoming whatever int32(0) happens to be.
			name: "on_classifier_error=UNSPECIFIED defensively maps to Passthrough",
			cfg: configpb.LlmClassifierConfig_builder{
				OnClassifierError: ptr(configpb.LlmClassifierConfig_ON_CLASSIFIER_ERROR_UNSPECIFIED),
			}.Build(),
			want: Config{
				Timeout:              8 * time.Second,
				MaxConsecutiveBlocks: 3,
				MaxSessionBlocks:     20,
				BackstopTtl:          30 * time.Minute,
				AutoModePolicyTtl:    24 * time.Hour,
				Mode:                 ModeFullAuto,
				OnClassifierError:    OnClassifierErrorPassthrough,
			},
		},
		{
			// Sentinel: explicit UNSPECIFIED mode defensively maps to FullAuto — a stray zero value must not silently strip
			// the classifier's approval authority.
			name: "mode=UNSPECIFIED defensively maps to FullAuto",
			cfg: configpb.LlmClassifierConfig_builder{
				Mode: ptr(configpb.LlmClassifierConfig_CLASSIFIER_MODE_UNSPECIFIED),
			}.Build(),
			want: Config{
				Timeout:              8 * time.Second,
				MaxConsecutiveBlocks: 3,
				MaxSessionBlocks:     20,
				BackstopTtl:          30 * time.Minute,
				AutoModePolicyTtl:    24 * time.Hour,
				Mode:                 ModeFullAuto,
				OnClassifierError:    OnClassifierErrorPassthrough,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ConfigFromProto(tt.cfg)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ConfigFromProto = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }
