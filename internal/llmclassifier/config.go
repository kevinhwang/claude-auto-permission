package llmclassifier

import "time"

// OnClassifierError selects the verdict the classifier returns on an infrastructure failure (provider outage,
// transcript read error, etc.). Mirrors the proto enum so consumers don't drag the proto type across the boundary.
type OnClassifierError int

const (
	// OnClassifierErrorPassthrough emits no wire output on failure; Claude Code's normal permission flow handles the call.
	// Default — accepts the risk that an overly broad Claude Code `permissions.allow` pattern could fast-path something
	// the classifier would have vetoed.
	OnClassifierErrorPassthrough OnClassifierError = iota
	// OnClassifierErrorAsk forces Claude Code to prompt on failure. Trades outage-time friction for safety against broad
	// allowlists.
	OnClassifierErrorAsk
)

// Mode bounds the verdicts the classifier may emit. Mirrors the proto enum so consumers don't drag the proto type
// across the boundary.
type Mode int

const (
	// ModeFullAuto lets the classifier both auto-approve aligned calls and block dangerous ones.
	ModeFullAuto Mode = iota
	// ModeBlockOnly restricts the classifier to denials: a no-block verdict is emitted as no opinion, deferring to peer
	// deciders and Claude Code's normal flow rather than asserting approval.
	ModeBlockOnly
)

// Config carries the per-project classifier knobs the [Decider] reads on each call. Field defaults come from
// `(opts).default` annotations on the proto schema and are applied by `loader.FillDefaults` inside [ConfigFromProto].
type Config struct {
	Enabled              bool
	Timeout              time.Duration
	MaxConsecutiveBlocks int
	MaxSessionBlocks     int
	BackstopTtl          time.Duration
	AutoModePolicyTtl    time.Duration
	Mode                 Mode
	OnClassifierError    OnClassifierError
}
