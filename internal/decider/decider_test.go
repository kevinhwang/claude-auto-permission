package decider

import "testing"

// TestCombine_WorkedTraces covers every row of the worked-trace table in docs (Decision combination — and the
// no-short-circuit-on-allow guarantee). The headline scenario the architecture exists for is "static=allow, llm=deny →
// deny": the LLM classifier sees a user-stated boundary in the transcript that the static engine cannot reason about,
// and its deny must beat the static allow.
func TestCombine_WorkedTraces(t *testing.T) {
	t.Parallel()

	r := func(name string, d Decision, reason string) Result {
		return Result{Name: name, Decision: d, Reason: reason}
	}

	tests := []struct {
		name       string
		results    []Result
		wantDec    Decision
		wantReason string
	}{
		{
			name:    "empty input → silent",
			results: nil,
			wantDec: DecisionSilent,
		},
		{
			name: "all silent → silent",
			results: []Result{
				r("static_bash_rules", DecisionSilent, "not bash"),
				r("llm_classifier", DecisionSilent, "skipped: classifier disabled"),
			},
			wantDec: DecisionSilent,
		},
		{
			name: "single allow",
			results: []Result{
				r("static_bash_rules", DecisionAllow, "git_status_allowlist"),
			},
			wantDec:    DecisionAllow,
			wantReason: "git_status_allowlist",
		},
		{
			name: "single deny",
			results: []Result{
				r("llm_classifier", DecisionDeny, "user said no"),
			},
			wantDec:    DecisionDeny,
			wantReason: "user said no",
		},
		{
			name: "allow + silent → allow (static-only happy path)",
			results: []Result{
				r("static_bash_rules", DecisionAllow, "git_status_allowlist"),
				r("llm_classifier", DecisionSilent, "skipped: classifier disabled"),
			},
			wantDec:    DecisionAllow,
			wantReason: "git_status_allowlist",
		},
		{
			name: "deny + silent → deny",
			results: []Result{
				r("static_bash_rules", DecisionDeny, "blocked by rule"),
				r("llm_classifier", DecisionSilent, ""),
			},
			wantDec:    DecisionDeny,
			wantReason: "blocked by rule",
		},
		{
			// HEADLINE SCENARIO. The whole architecture exists for this row. Static engine matches its allowlist on `git push`
			// but the LLM has read the transcript and seen "don't push to main" — its deny MUST win. If this test fails, the
			// no-short-circuit-on-allow guarantee has been broken.
			name: "allow + deny → deny (LLM veto over static allow)",
			results: []Result{
				r("static_bash_rules", DecisionAllow, "git_push_subcommand_allowlist"),
				r("llm_classifier", DecisionDeny, "user explicitly forbade git push"),
			},
			wantDec:    DecisionDeny,
			wantReason: "user explicitly forbade git push",
		},
		{
			// The reverse ordering — deny first, allow second — short-circuits on the deny but produces the same final decision.
			// We pass the static-engine's reason because Combine returns the first deny's reason, not "the most informative"
			// one.
			name: "deny + allow → deny (deny short-circuits)",
			results: []Result{
				r("static_bash_rules", DecisionDeny, "blocked by rule"),
				r("llm_classifier", DecisionAllow, ""),
			},
			wantDec:    DecisionDeny,
			wantReason: "blocked by rule",
		},
		{
			name: "silent + allow → allow (LLM-only happy path on non-Bash)",
			results: []Result{
				r("static_bash_rules", DecisionSilent, "not bash"),
				r("llm_classifier", DecisionAllow, ""),
			},
			wantDec: DecisionAllow,
		},
		{
			name: "silent + deny → deny",
			results: []Result{
				r("static_bash_rules", DecisionSilent, "not bash"),
				r("llm_classifier", DecisionDeny, "untrusted external code"),
			},
			wantDec:    DecisionDeny,
			wantReason: "untrusted external code",
		},
		{
			// Three-decider ordering: a hypothetical future external policy decider before the existing two. Allow + Allow +
			// Deny still resolves to Deny.
			name: "three-way: allow + allow + deny → deny",
			results: []Result{
				r("external_policy", DecisionAllow, "policy ok"),
				r("static_bash_rules", DecisionAllow, "git_push_subcommand_allowlist"),
				r("llm_classifier", DecisionDeny, "user said no"),
			},
			wantDec:    DecisionDeny,
			wantReason: "user said no",
		},
		{
			// Three-way silent + silent + allow → allow with the lone allow's reason.
			name: "three-way: silent + silent + allow → allow",
			results: []Result{
				r("external_policy", DecisionSilent, "no opinion"),
				r("static_bash_rules", DecisionSilent, "not bash"),
				r("llm_classifier", DecisionAllow, "approved"),
			},
			wantDec:    DecisionAllow,
			wantReason: "approved",
		},
		{
			// First-allow wins for the reason field when multiple deciders vote allow. The first-wins ordering matters because
			// users reading the log will expect the highest-priority decider (registered first) to attribute the allow.
			name: "two allows → allow with first decider's reason",
			results: []Result{
				r("static_bash_rules", DecisionAllow, "git_status_allowlist"),
				r("llm_classifier", DecisionAllow, "classifier approved"),
			},
			wantDec:    DecisionAllow,
			wantReason: "git_status_allowlist",
		},

		// ── DecisionAsk precedence ──────────────────────────────────

		{
			name: "single ask",
			results: []Result{
				r("llm_classifier", DecisionAsk, "backstop: 3 consecutive blocks"),
			},
			wantDec:    DecisionAsk,
			wantReason: "backstop: 3 consecutive blocks",
		},
		{
			name: "ask + silent → ask",
			results: []Result{
				r("llm_classifier", DecisionAsk, "backstop tripped"),
				r("future_decider", DecisionSilent, "no opinion"),
			},
			wantDec:    DecisionAsk,
			wantReason: "backstop tripped",
		},
		{
			// THE BUG FIX SCENARIO. Static engine matches its allowlist but the backstop has tripped — ask MUST win over allow.
			// Previously this resolved to allow, letting dangerous commands through when the classifier was disabled.
			name: "allow + ask → ask (backstop overrides static allow)",
			results: []Result{
				r("static_bash_rules", DecisionAllow, "git_status_allowlist"),
				r("llm_classifier", DecisionAsk, "backstop: 3 consecutive blocks"),
			},
			wantDec:    DecisionAsk,
			wantReason: "backstop: 3 consecutive blocks",
		},
		{
			name: "ask + allow → ask (ordering doesn't matter)",
			results: []Result{
				r("llm_classifier", DecisionAsk, "backstop tripped"),
				r("static_bash_rules", DecisionAllow, "allowlisted"),
			},
			wantDec:    DecisionAsk,
			wantReason: "backstop tripped",
		},
		{
			name: "deny + ask → deny (deny still wins)",
			results: []Result{
				r("static_bash_rules", DecisionDeny, "explicit deny rule"),
				r("llm_classifier", DecisionAsk, "backstop tripped"),
			},
			wantDec:    DecisionDeny,
			wantReason: "explicit deny rule",
		},
		{
			name: "ask + deny → deny (deny always wins regardless of order)",
			results: []Result{
				r("llm_classifier", DecisionAsk, "backstop tripped"),
				r("future_decider", DecisionDeny, "policy violation"),
			},
			wantDec:    DecisionDeny,
			wantReason: "policy violation",
		},
		{
			name: "three-way: allow + ask + deny → deny",
			results: []Result{
				r("static_bash_rules", DecisionAllow, "allowlisted"),
				r("llm_classifier", DecisionAsk, "backstop"),
				r("policy_engine", DecisionDeny, "blocked"),
			},
			wantDec:    DecisionDeny,
			wantReason: "blocked",
		},

		// ── DecisionPassthrough precedence ────────────────────────── Passthrough means "I should have weighed in but my
		// verdict is unavailable" (e.g. classifier provider outage). Beats Allow so a peer decider's permissive vote can't
		// auto-approve in our absence; loses to Ask and Deny so explicit human review or an authoritative block always wins.

		{
			name: "single passthrough",
			results: []Result{
				r("llm_classifier", DecisionPassthrough, "bedrock: timeout"),
			},
			wantDec:    DecisionPassthrough,
			wantReason: "bedrock: timeout",
		},
		{
			name: "passthrough + silent → passthrough",
			results: []Result{
				r("llm_classifier", DecisionPassthrough, "bedrock: timeout"),
				r("future_decider", DecisionSilent, "no opinion"),
			},
			wantDec:    DecisionPassthrough,
			wantReason: "bedrock: timeout",
		},
		{
			// HEADLINE SCENARIO for the passthrough verdict. Static engine's allowlist would have approved `git push`, but the
			// LLM is incapacitated and refuses to let an Allow stand without its semantic check. Passthrough wins, emitting no
			// wire output so Claude Code's normal flow runs (which will prompt unless the user has an explicit allow).
			name: "allow + passthrough → passthrough (LLM withholds verdict)",
			results: []Result{
				r("static_bash_rules", DecisionAllow, "git_push_subcommand_allowlist"),
				r("llm_classifier", DecisionPassthrough, "bedrock: timeout"),
			},
			wantDec:    DecisionPassthrough,
			wantReason: "bedrock: timeout",
		},
		{
			name: "passthrough + allow → passthrough (ordering doesn't matter)",
			results: []Result{
				r("llm_classifier", DecisionPassthrough, "bedrock: timeout"),
				r("static_bash_rules", DecisionAllow, "allowlisted"),
			},
			wantDec:    DecisionPassthrough,
			wantReason: "bedrock: timeout",
		},
		{
			name: "ask + passthrough → ask (Ask still wins)",
			results: []Result{
				r("llm_classifier", DecisionAsk, "backstop tripped"),
				r("future_decider", DecisionPassthrough, "outage"),
			},
			wantDec:    DecisionAsk,
			wantReason: "backstop tripped",
		},
		{
			name: "passthrough + ask → ask (ordering doesn't matter)",
			results: []Result{
				r("future_decider", DecisionPassthrough, "outage"),
				r("llm_classifier", DecisionAsk, "backstop tripped"),
			},
			wantDec:    DecisionAsk,
			wantReason: "backstop tripped",
		},
		{
			name: "passthrough + deny → deny (Deny always wins)",
			results: []Result{
				r("llm_classifier", DecisionPassthrough, "outage"),
				r("policy_engine", DecisionDeny, "blocked"),
			},
			wantDec:    DecisionDeny,
			wantReason: "blocked",
		},
		{
			name: "three-way: allow + passthrough + ask → ask",
			results: []Result{
				r("static_bash_rules", DecisionAllow, "allowlisted"),
				r("llm_classifier", DecisionPassthrough, "outage"),
				r("policy_engine", DecisionAsk, "policy review"),
			},
			wantDec:    DecisionAsk,
			wantReason: "policy review",
		},
		{
			// Multiple passthroughs: first-wins for the reason, matching Allow/Ask behavior.
			name: "two passthroughs → passthrough with first decider's reason",
			results: []Result{
				r("llm_classifier", DecisionPassthrough, "bedrock: timeout"),
				r("policy_engine", DecisionPassthrough, "policy server down"),
			},
			wantDec:    DecisionPassthrough,
			wantReason: "bedrock: timeout",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotDec, gotReason := Combine(tt.results)
			if gotDec != tt.wantDec {
				t.Errorf("Combine decision = %q, want %q", gotDec, tt.wantDec)
			}
			if gotReason != tt.wantReason {
				t.Errorf("Combine reason = %q, want %q", gotReason, tt.wantReason)
			}
		})
	}
}
