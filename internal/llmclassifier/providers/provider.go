// Package providers defines the contract a classifier-LLM provider must implement. Concrete implementations live under
// subpackages (bedrock/ for direct Bedrock).
package providers

import (
	"context"
	"encoding/json"
)

// Provider produces a structured classifier verdict from a system prompt and a user prompt.
//
// A returned error means the classifier was *unavailable* (timeout, parse failure, model outage), not a deny verdict.
// Callers map that to silent fall-through.
//
//go:generate go run go.uber.org/mock/mockgen@v0.6.0 -typed -source=provider.go -destination=mocks/provider_mock.go -package=mocks
type Provider interface {
	// Name returns a short tag for logging/cache-keying.
	Name() string

	// Model returns the configured model identifier; empty when the provider delegates to a remote default. Surfaced in
	// the decision log to correlate verdict quality with model choice.
	Model() string

	// Classify runs one classification round. Schema constrains the structured output to {thinking, shouldBlock, reason}.
	Classify(ctx context.Context, req Request) (Result, error)
}

// Request is the input to one classification call.
type Request struct {
	SystemPrompt string
	// UserPrefix is an optional user-role API message sent before the transcript. Carries the resolved CLAUDE.md (with
	// @-imports inlined) as a separate {role:user} turn. Empty = no prefix.
	UserPrefix string
	UserPrompt string
	Schema     json.RawMessage
}

// Result is the parsed structured output plus diagnostic metadata.
type Result struct {
	ShouldBlock bool
	Reason      string

	// LatencyMs is the wall-clock cost of Classify.
	LatencyMs int

	// RawResponse is the unparsed assistant output, for debugging. May be empty when the provider parses out of a
	// structured channel.
	RawResponse []byte
}

// UnavailableReason tags why a provider returned an error. Concrete providers wrap their failures in [Error] so callers
// (today only the classifier; future metrics consumers as well) can distinguish timeout from prompt-too-long from
// generic outage.
type UnavailableReason string

const (
	UnavailableTimeout     UnavailableReason = "timeout"
	UnavailableParse       UnavailableReason = "parse"
	UnavailableTooLong     UnavailableReason = "transcript_too_long"
	UnavailableProviderErr UnavailableReason = "provider_error"
)

// Error is the typed error a [Provider] returns for unavailability.
type Error struct {
	Reason UnavailableReason
	Err    error
}

func (e *Error) Error() string {
	if e.Err == nil {
		return string(e.Reason)
	}
	return string(e.Reason) + ": " + e.Err.Error()
}

func (e *Error) Unwrap() error { return e.Err }
