package decider

// Stable identifiers for every registered decider. They appear as JSON keys in the decision log; renaming any of them
// is a breaking change to log consumers.
//
// Listing the names centrally keeps the orchestrator + decisionlog + tests on one canonical source — and acts as an
// enum of the judges currently wired into the binary.
const (
	NameStaticBash    = "static_bash_rules"
	NameLlmClassifier = "llm_classifier"
)
