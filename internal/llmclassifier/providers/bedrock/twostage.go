package bedrock

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	"claude-auto-permission/internal/llmclassifier/providers"
)

// Two-stage classification mirrors Claude Code's reference impl.
//
// Stage 1: Converse call with a tight max_tokens budget and a `</block>` stop sequence — ~5 output tokens on allow,
// aborts after. Stage 2 (TwoStageBoth, on stage-1 block or parse failure): another Converse call asking for
// `<thinking>` chain-of-thought before the verdict, for the authoritative reason.
//
// Both stages are non-streaming. stop_sequences abort generation server-side; the latency win is in fewer generated
// tokens, not incremental wire delivery.
//
// System prompt is rewritten to ask for XML; user body wraps in `<transcript>` with an XML_S{1,2}_SUFFIX nudge.
// Cache_point blocks terminate both system and user content so stage 2 reuses stage 1's prefix from Bedrock's prompt
// cache.

// xmlOutputFormat replaces the structured-output instruction line in the system prompt with XML instructions.
//
// The "begin with <block>" rule is intentionally strict: softening it makes the model burn its short max_tokens budget
// on reasoning before the verdict, leaving stage 1 (especially Fast at 256 tokens) unparseable. The stage-2 user suffix
// relaxes this — user > system in instruction priority.
const xmlOutputFormat = `## Output Format

If the action should be blocked:
<block>yes</block><reason>one short sentence</reason>

If the action should be allowed:
<block>no</block>

Do NOT include a <reason> tag when the action is allowed.
Your ENTIRE response MUST begin with <block>. Do NOT output any analysis, reasoning, or commentary before <block>. No "Looking at..." or similar preamble.`

// systemPromptOutputLine is the verbatim structured-output instruction emitted by the bundled system template;
// rewriteSystemForXml swaps it for xmlOutputFormat. Must stay in lockstep with
// internal/llmclassifier/prompt/builtin/prompts.md.tmpl. Duplicated here because the bedrock package can't import
// prompt without circularity.
const systemPromptOutputLine = `Use the classify_result tool to report your classification.`

// xmlS1Suffix nudges an immediate `<block>` verdict from stage 1.
const xmlS1Suffix = "\nErr on the side of blocking. <block> immediately."

// xmlS2Suffix elicits chain-of-thought before the stage-2 verdict.
const xmlS2Suffix = "\nReview the classification process and follow it carefully, making sure you deny actions that should be blocked. As a reminder, explicit (not suggestive or implicit) user confirmation is required to override blocks. Use <thinking> before responding with <block>."

const stage1StopSequence = "</block>"

// xmlBlockRegex tolerates a missing closing tag because Bedrock strips the stop sequence from the response (content
// ends `<block>no`).
var xmlBlockRegex = regexp.MustCompile(`(?i)<block>\s*(yes|no)\b\s*(?:</block>)?`)

// xmlReasonRegex extracts the optional one-sentence reason. Stage 1 Both never produces this (aborts on `</block>`);
// stage 1 Fast and stage 2 do.
var xmlReasonRegex = regexp.MustCompile(`(?s)<reason>\s*(.*?)\s*</reason>`)

// Strip thinking blocks (and unclosed tails) before parsing so tags inside chain-of-thought aren't mistaken for the
// verdict.
var xmlThinkingRegex = regexp.MustCompile(`(?s)<thinking>.*?</thinking>`)
var xmlPartialThinkingRegex = regexp.MustCompile(`(?s)<thinking>.*$`)

func stripThinking(s string) string {
	s = xmlThinkingRegex.ReplaceAllString(s, "")
	s = xmlPartialThinkingRegex.ReplaceAllString(s, "")
	return s
}

// parseXMLBlock returns (true, true) for `<block>yes…`, (false, true) for `<block>no…`, and (false, false) when no
// parseable verdict. Callers decide whether unparseable means escalate (Both) or block-for-safety (Fast).
func parseXMLBlock(s string) (block bool, ok bool) {
	m := xmlBlockRegex.FindStringSubmatch(stripThinking(s))
	if m == nil {
		return false, false
	}
	return strings.EqualFold(m[1], "yes"), true
}

func parseXMLReason(s string) string {
	m := xmlReasonRegex.FindStringSubmatch(stripThinking(s))
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// rewriteSystemForXml swaps the structured-output line for xmlOutputFormat. If the line isn't present, xmlOutputFormat
// is appended at the tail.
func rewriteSystemForXml(s string) string {
	if strings.Contains(s, systemPromptOutputLine) {
		return strings.Replace(s, systemPromptOutputLine, xmlOutputFormat, 1)
	}
	return s + "\n\n" + xmlOutputFormat
}

// classifyTwoStage runs stage 1 and either short-circuits on allow, finalizes on block in Fast mode, or escalates to
// stage 2 in Both mode.
func (p *Provider) classifyTwoStage(ctx context.Context, req providers.Request) (providers.Result, error) {
	xmlSystem := rewriteSystemForXml(req.SystemPrompt)
	stage1Body := wrapTranscript(req.UserPrompt, xmlS1Suffix)

	stage1MaxTokens := p.cfg.Stage1MaxTokensBoth
	stopSequences := []string{stage1StopSequence}
	if p.cfg.TwoStage == TwoStageFast {
		// Stage 1 is final in Fast: must carry both verdict and reason. Drop the stop sequence so `<reason>` can follow.
		stage1MaxTokens = p.cfg.Stage1MaxTokensFast
		stopSequences = nil
	}

	stage1Input := &bedrockruntime.ConverseInput{
		ModelId:           aws.String(p.cfg.ModelId),
		System:            cachedSystemBlocks(xmlSystem),
		Messages:          userMessages(req.UserPrefix, stage1Body),
		InferenceConfig:   p.inferenceConfig(stage1MaxTokens, stopSequences),
		PerformanceConfig: p.performanceConfig(),
	}

	stage1Start := time.Now()
	out, err := p.client.Converse(ctx, stage1Input)
	stage1Ms := int(time.Since(stage1Start) / time.Millisecond)
	if err != nil {
		return providers.Result{LatencyMs: stage1Ms}, classifyErr(ctx, err, stage1Ms)
	}
	stage1Text := extractConverseText(out)

	block, ok := parseXMLBlock(stage1Text)
	switch {
	case ok && !block:
		// Stage 1 allow: short-circuit, the latency win.
		return providers.Result{
			ShouldBlock: false,
			RawResponse: []byte(stage1Text),
			LatencyMs:   stage1Ms,
		}, nil

	case ok && block && p.cfg.TwoStage == TwoStageFast:
		// Stage 1 is final in Fast — no chain-of-thought, reason quality is best-effort.
		reason := parseXMLReason(stage1Text)
		if reason == "" {
			reason = "Blocked by fast classifier"
		}
		return providers.Result{
			ShouldBlock: true,
			Reason:      reason,
			RawResponse: []byte(stage1Text),
			LatencyMs:   stage1Ms,
		}, nil

	case !ok && p.cfg.TwoStage == TwoStageFast:
		// Parse failure with no verdict — map to silent + unavailable so the user isn't blocked on a classifier error.
		return providers.Result{LatencyMs: stage1Ms}, &providers.Error{
			Reason: providers.UnavailableParse,
			Err:    fmt.Errorf("stage 1 unparseable: %q", truncate(stage1Text, 200)),
		}
	}

	// Both, on stage-1 block or parse failure: escalate. Sharing the XML format keeps stage 2 hitting Bedrock's cache for
	// stage 1's prefix.
	stage2Result, stage2Err := p.classifyXml(ctx, req, xmlS2Suffix, p.maxTokens(), nil)
	stage2Result.LatencyMs += stage1Ms
	if stage2Err != nil {
		return stage2Result, stage2Err
	}
	return stage2Result, nil
}

// classifyXml runs one XML-format Converse call: TwoStageThinking (single call) or TwoStageBoth stage 2 (escalation).
// Both share the XML system prompt and stage-2 suffix so they reuse Bedrock's prompt cache; switching to forced tool
// use mid-flow would invalidate it.
func (p *Provider) classifyXml(
	ctx context.Context,
	req providers.Request,
	suffix string,
	maxTokens int32,
	stopSequences []string,
) (providers.Result, error) {
	xmlSystem := rewriteSystemForXml(req.SystemPrompt)
	body := wrapTranscript(req.UserPrompt, suffix)

	input := &bedrockruntime.ConverseInput{
		ModelId:           aws.String(p.cfg.ModelId),
		System:            cachedSystemBlocks(xmlSystem),
		Messages:          userMessages(req.UserPrefix, body),
		InferenceConfig:   p.inferenceConfig(maxTokens, stopSequences),
		PerformanceConfig: p.performanceConfig(),
	}

	start := time.Now()
	out, err := p.client.Converse(ctx, input)
	latency := int(time.Since(start) / time.Millisecond)
	if err != nil {
		return providers.Result{LatencyMs: latency}, classifyErr(ctx, err, latency)
	}

	text := extractConverseText(out)
	block, ok := parseXMLBlock(text)
	if !ok {
		return providers.Result{LatencyMs: latency}, &providers.Error{
			Reason: providers.UnavailableParse,
			Err:    fmt.Errorf("XML stage unparseable: %q", truncate(text, 200)),
		}
	}
	reason := parseXMLReason(text)
	if block && reason == "" {
		reason = "Blocked by classifier"
	}
	return providers.Result{
		ShouldBlock: block,
		Reason:      reason,
		RawResponse: []byte(text),
		LatencyMs:   latency,
	}, nil
}

// extractConverseText concatenates text blocks from a Converse response. Returns "" on non-message output; callers
// surface that as an unavailable-parse error.
func extractConverseText(out *bedrockruntime.ConverseOutput) string {
	if out == nil || out.Output == nil {
		return ""
	}
	msg, ok := out.Output.(*types.ConverseOutputMemberMessage)
	if !ok {
		return ""
	}
	var b strings.Builder
	for _, block := range msg.Value.Content {
		t, ok := block.(*types.ContentBlockMemberText)
		if !ok {
			continue
		}
		b.WriteString(t.Value)
	}
	return b.String()
}

// wrapTranscript wraps the body in `<transcript>` and appends suffix.
func wrapTranscript(body, suffix string) string {
	return "<transcript>\n" + body + "\n</transcript>\n" + suffix
}

// 1h covers the natural minute-long pauses while the user reads output, keeping the prefix warm across them. No-op for
// models / sizes where caching doesn't fire.
const cacheTTL = types.CacheTTLOneHour

func cachedSystemBlocks(text string) []types.SystemContentBlock {
	if text == "" {
		return nil
	}
	return []types.SystemContentBlock{
		&types.SystemContentBlockMemberText{Value: text},
		&types.SystemContentBlockMemberCachePoint{
			Value: types.CachePointBlock{Type: types.CachePointTypeDefault, Ttl: cacheTTL},
		},
	}
}

func cachedUserContent(text string) []types.ContentBlock {
	return []types.ContentBlock{
		&types.ContentBlockMemberText{Value: text},
		&types.ContentBlockMemberCachePoint{
			Value: types.CachePointBlock{Type: types.CachePointTypeDefault, Ttl: cacheTTL},
		},
	}
}

// userMessages emits CLAUDE.md (when present) as a separate user-role message before the transcript, each terminated
// with its own cache_point so the prefix reuses across calls within a session.
func userMessages(prefix, body string) []types.Message {
	var msgs []types.Message
	if prefix != "" {
		msgs = append(msgs, types.Message{
			Role:    types.ConversationRoleUser,
			Content: cachedUserContent(prefix),
		})
	}
	msgs = append(msgs, types.Message{
		Role:    types.ConversationRoleUser,
		Content: cachedUserContent(body),
	})
	return msgs
}

// truncate clips noisy model responses for inclusion in error messages and the user-visible deny reason.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
