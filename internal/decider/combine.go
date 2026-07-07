package decider

// Combine reduces an ordered list of [Result]s into a final decision and reason.
//
// Precedence: `deny > ask > passthrough > allow > silent`. Notes:
//
//   - Only `deny` short-circuits — once any decider denies, nothing later can override.
//   - The other levels never short-circuit so a later veto can override an earlier permissive vote (the whole point of
//     having multiple deciders).
//   - `passthrough` beats `allow` so a decider that should have weighed in but couldn't (e.g. the LLM classifier when
//     its provider is down) doesn't let a peer's `allow` auto-approve in its absence.
//
// Returns `(DecisionSilent, "")` when `results` is empty or every vote was silent.
func Combine(results []Result) (Decision, string) {
	var firstAsk, firstPassthrough, firstAllow *Result
	for i := range results {
		switch results[i].Decision {
		case DecisionDeny:
			return DecisionDeny, results[i].Reason
		case DecisionAsk:
			if firstAsk == nil {
				firstAsk = &results[i]
			}
		case DecisionPassthrough:
			if firstPassthrough == nil {
				firstPassthrough = &results[i]
			}
		case DecisionAllow:
			if firstAllow == nil {
				firstAllow = &results[i]
			}
		}
	}
	if firstAsk != nil {
		return DecisionAsk, firstAsk.Reason
	}
	if firstPassthrough != nil {
		return DecisionPassthrough, firstPassthrough.Reason
	}
	if firstAllow != nil {
		return DecisionAllow, firstAllow.Reason
	}
	return DecisionSilent, ""
}
