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

// Config carries the per-project classifier knobs the [Decider] reads on each call. Field defaults come from
// `(opts).default` annotations on the proto schema and are applied by `loader.FillDefaults` inside [ConfigFromProto].
type Config struct {
	Enabled              bool
	Timeout              time.Duration
	MaxConsecutiveBlocks int
	MaxSessionBlocks     int
	BackstopTtl          time.Duration
	AutoModePolicyTtl    time.Duration
	Stage1MaxTokensBoth  int32
	Stage1MaxTokensFast  int32
	OnClassifierError    OnClassifierError
}
