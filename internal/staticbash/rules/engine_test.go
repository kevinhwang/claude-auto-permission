package rules

import (
	"strings"
	"testing"

	rulespb "claude-auto-permission/internal/gen/rules/v1"

	"mvdan.cc/sh/v3/syntax"
)

// --------------------------------------------------------------------------- Helpers
// ---------------------------------------------------------------------------

func parseWords(t *testing.T, cmd string) []*syntax.Word {
	t.Helper()
	f, err := syntax.NewParser().Parse(strings.NewReader(cmd), "")
	if err != nil {
		t.Fatalf("syntax.Parse(%q): %v", cmd, err)
	}
	if len(f.Stmts) == 0 {
		return nil
	}
	if len(f.Stmts) != 1 {
		t.Fatalf("expected 1 stmt, got %d", len(f.Stmts))
	}
	call, ok := f.Stmts[0].Cmd.(*syntax.CallExpr)
	if !ok {
		t.Fatalf("expected CallExpr, got %T", f.Stmts[0].Cmd)
	}
	return call.Args
}

func defaultCtx() *EvalCtx {
	return &EvalCtx{
		Cwd:           "/project",
		IsPathAllowed: func(path string) bool { return strings.HasPrefix(path, "/allowed/") },
		Evaluate:      func(cmd, cwd string, dirs []string) bool { return true },
	}
}

// makeSpec builds a CommandSpec with the given names and rules.
func makeSpec(names []string, rules []*rulespb.Rule, opts ...func(*rulespb.CustomRules)) *rulespb.CommandSpec {
	cr := rulespb.CustomRules_builder{Rules: rules}.Build()
	for _, opt := range opts {
		opt(cr)
	}
	return rulespb.CommandSpec_builder{
		Names:       names,
		CustomRules: cr,
	}.Build()
}

func allowRule(cond *rulespb.Condition) *rulespb.Rule {
	return rulespb.Rule_builder{Allow: rulespb.Allow_builder{Condition: cond}.Build()}.Build()
}

func denyRule(cond *rulespb.Condition) *rulespb.Rule {
	return rulespb.Rule_builder{Deny: rulespb.Deny_builder{Condition: cond}.Build()}.Build()
}

func alwaysAllowRule() *rulespb.Rule {
	return rulespb.Rule_builder{AlwaysAllow: rulespb.AlwaysAllow_builder{}.Build()}.Build()
}

func alwaysCond() *rulespb.Condition {
	return rulespb.Condition_builder{Always: rulespb.Always_builder{}.Build()}.Build()
}

func notCond(inner *rulespb.Condition) *rulespb.Condition {
	return rulespb.Condition_builder{Not: rulespb.Not_builder{Condition: inner}.Build()}.Build()
}

func noArgsCond() *rulespb.Condition {
	return rulespb.Condition_builder{NoArgs: rulespb.NoArgs_builder{}.Build()}.Build()
}

func hasDoubleDashCond() *rulespb.Condition {
	return rulespb.Condition_builder{HasDoubleDash: rulespb.HasDoubleDash_builder{}.Build()}.Build()
}

func hasFlagExact(flags ...string) *rulespb.Condition {
	return rulespb.Condition_builder{
		HasFlagMatching: rulespb.HasFlagMatching_builder{
			Exact: rulespb.ExactFlags_builder{Flags: flags}.Build(),
		}.Build(),
	}.Build()
}

func hasFlagPattern(shortChars string, longPrefixes ...string) *rulespb.Condition {
	sc := shortChars
	return rulespb.Condition_builder{
		HasFlagMatching: rulespb.HasFlagMatching_builder{
			Pattern: rulespb.PatternFlags_builder{
				ShortChars:   &sc,
				LongPrefixes: longPrefixes,
			}.Build(),
		}.Build(),
	}.Build()
}

func flagArgCheckCond(names []string, hasArg bool, chk *rulespb.Check) *rulespb.Condition {
	ha := hasArg
	return rulespb.Condition_builder{
		FlagArgCheck: rulespb.FlagArgCheck_builder{
			Names:  names,
			HasArg: &ha,
			Check:  chk,
		}.Build(),
	}.Build()
}

func everyFlagMatchesCond(noArgs, withArgs []string) *rulespb.Condition {
	return rulespb.Condition_builder{
		EveryFlagMatches: rulespb.EveryFlagMatches_builder{
			AllowedWithoutArgs: noArgs,
			AllowedWithArgs:    withArgs,
		}.Build(),
	}.Build()
}

func everyPositionalPassesCond(chk *rulespb.Check) *rulespb.Condition {
	return rulespb.Condition_builder{
		EveryPositionalPasses: rulespb.EveryPositionalPasses_builder{Check: chk}.Build(),
	}.Build()
}

func subcommandCond(allowedAny []string, withRules ...*rulespb.SubcommandEntry) *rulespb.Condition {
	return rulespb.Condition_builder{
		Subcommands: rulespb.SubcommandCheck_builder{
			AllowedWithAnyArgs: allowedAny,
			WithRules:          withRules,
		}.Build(),
	}.Build()
}

func maxPositionalsCond(n int32) *rulespb.Condition {
	count := n
	return rulespb.Condition_builder{
		MaxPositionals: rulespb.MaxPositionals_builder{Count: &count}.Build(),
	}.Build()
}

func writeCheck() *rulespb.Check {
	return rulespb.Check_builder{WriteCheck: rulespb.WriteCheck_builder{}.Build()}.Build()
}

func recurseEvalCheck() *rulespb.Check {
	return rulespb.Check_builder{RecurseEval: rulespb.RecurseEval_builder{}.Build()}.Build()
}

// --------------------------------------------------------------------------- Rule Semantics
// ---------------------------------------------------------------------------

func TestRuleSemantics(t *testing.T) {
	t.Run("AllowAlways passes", func(t *testing.T) {
		spec := makeSpec([]string{"cmd"}, []*rulespb.Rule{allowRule(alwaysCond())})
		got := Evaluate(spec, defaultCtx(), parseWords(t, "cmd"))
		if !got {
			t.Fatal("expected allowed")
		}
	})

	t.Run("Deny condition blocks", func(t *testing.T) {
		spec := makeSpec([]string{"cmd"}, []*rulespb.Rule{
			allowRule(alwaysCond()),
			denyRule(alwaysCond()),
		})
		got := Evaluate(spec, defaultCtx(), parseWords(t, "cmd"))
		if got {
			t.Fatal("expected denied")
		}
	})

	t.Run("Multiple allows all must pass", func(t *testing.T) {
		// Two allows: Always (passes) and NoArgs (fails because we pass an arg).
		spec := makeSpec([]string{"cmd"}, []*rulespb.Rule{
			allowRule(alwaysCond()),
			allowRule(noArgsCond()),
		})
		got := Evaluate(spec, defaultCtx(), parseWords(t, "cmd foo"))
		if got {
			t.Fatal("expected denied when second allow fails")
		}
	})

	t.Run("Multiple allows all pass", func(t *testing.T) {
		spec := makeSpec([]string{"cmd"}, []*rulespb.Rule{
			allowRule(alwaysCond()),
			allowRule(alwaysCond()),
		})
		got := Evaluate(spec, defaultCtx(), parseWords(t, "cmd"))
		if !got {
			t.Fatal("expected allowed")
		}
	})

	t.Run("No allows means denied", func(t *testing.T) {
		// Only a deny rule that doesn't trigger — still no allow → denied.
		spec := makeSpec([]string{"cmd"}, []*rulespb.Rule{
			denyRule(noArgsCond()), // not triggered because we pass an arg
		})
		got := Evaluate(spec, defaultCtx(), parseWords(t, "cmd foo"))
		if got {
			t.Fatal("expected denied when no allow rules exist")
		}
	})

	t.Run("Empty rules denied", func(t *testing.T) {
		spec := makeSpec([]string{"cmd"}, nil)
		got := Evaluate(spec, defaultCtx(), parseWords(t, "cmd"))
		if got {
			t.Fatal("expected denied with empty rules")
		}
	})

	t.Run("require_cwd blocks when cwd empty", func(t *testing.T) {
		spec := makeSpec([]string{"cmd"}, []*rulespb.Rule{allowRule(alwaysCond())},
			func(cr *rulespb.CustomRules) { cr.SetRequireCwd(true) })
		ctx := defaultCtx()
		ctx.Cwd = ""
		got := Evaluate(spec, ctx, parseWords(t, "cmd"))
		if got {
			t.Fatal("expected denied with empty cwd and require_cwd")
		}
	})

	t.Run("require_cwd passes when cwd set", func(t *testing.T) {
		spec := makeSpec([]string{"cmd"}, []*rulespb.Rule{allowRule(alwaysCond())},
			func(cr *rulespb.CustomRules) { cr.SetRequireCwd(true) })
		got := Evaluate(spec, defaultCtx(), parseWords(t, "cmd"))
		if !got {
			t.Fatal("expected allowed with cwd set and require_cwd")
		}
	})

	t.Run("Unknown checker type denied", func(t *testing.T) {
		spec := rulespb.CommandSpec_builder{Names: []string{"cmd"}}.Build()
		got := Evaluate(spec, defaultCtx(), parseWords(t, "cmd"))
		if got {
			t.Fatal("expected denied for nil checker")
		}
	})
}

// --------------------------------------------------------------------------- Condition: Always
// ---------------------------------------------------------------------------

func TestConditionAlways(t *testing.T) {
	spec := makeSpec([]string{"cmd"}, []*rulespb.Rule{allowRule(alwaysCond())})

	for _, cmd := range []string{"cmd", "cmd foo", "cmd -v", "cmd --flag arg"} {
		t.Run(cmd, func(t *testing.T) {
			if !Evaluate(spec, defaultCtx(), parseWords(t, cmd)) {
				t.Fatalf("expected allowed for %q", cmd)
			}
		})
	}
}

// --------------------------------------------------------------------------- Condition: Not
// ---------------------------------------------------------------------------

func TestConditionNot(t *testing.T) {
	t.Run("Not(Always) denies", func(t *testing.T) {
		spec := makeSpec([]string{"cmd"}, []*rulespb.Rule{allowRule(notCond(alwaysCond()))})
		if Evaluate(spec, defaultCtx(), parseWords(t, "cmd")) {
			t.Fatal("expected denied")
		}
	})

	t.Run("Not(NoArgs) passes with args", func(t *testing.T) {
		spec := makeSpec([]string{"cmd"}, []*rulespb.Rule{allowRule(notCond(noArgsCond()))})
		if !Evaluate(spec, defaultCtx(), parseWords(t, "cmd foo")) {
			t.Fatal("expected allowed")
		}
	})

	t.Run("Deny Not(HasFlag) blocks when flag absent", func(t *testing.T) {
		// Deny(Not(HasFlag(-v))) → deny when -v is absent.
		spec := makeSpec([]string{"cmd"}, []*rulespb.Rule{
			allowRule(alwaysCond()),
			denyRule(notCond(hasFlagExact("-v"))),
		})
		if Evaluate(spec, defaultCtx(), parseWords(t, "cmd")) {
			t.Fatal("expected denied when -v absent")
		}
		if !Evaluate(spec, defaultCtx(), parseWords(t, "cmd -v")) {
			t.Fatal("expected allowed when -v present")
		}
	})
}

// --------------------------------------------------------------------------- Condition: NoArgs
// ---------------------------------------------------------------------------

func TestConditionNoArgs(t *testing.T) {
	spec := makeSpec([]string{"cmd"}, []*rulespb.Rule{allowRule(noArgsCond())})

	tests := []struct {
		cmd  string
		want bool
	}{
		{"cmd", true},
		{"cmd foo", false},
		{"cmd -v", false},
		{"cmd -- bar", false},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			got := Evaluate(spec, defaultCtx(), parseWords(t, tt.cmd))
			if got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// --------------------------------------------------------------------------- Condition: HasDoubleDash
// ---------------------------------------------------------------------------

func TestConditionHasDoubleDash(t *testing.T) {
	spec := makeSpec([]string{"cmd"}, []*rulespb.Rule{allowRule(hasDoubleDashCond())})

	tests := []struct {
		cmd  string
		want bool
	}{
		{"cmd -- foo", true},
		{"cmd foo -- bar", true},
		{"cmd foo", false},
		{"cmd", false},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			got := Evaluate(spec, defaultCtx(), parseWords(t, tt.cmd))
			if got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// --------------------------------------------------------------------------- Condition: HasFlagMatching
// ---------------------------------------------------------------------------

func TestConditionHasFlagExact(t *testing.T) {
	spec := makeSpec([]string{"cmd"}, []*rulespb.Rule{allowRule(hasFlagExact("-v", "--verbose"))})

	tests := []struct {
		cmd  string
		want bool
	}{
		{"cmd -v", true},
		{"cmd --verbose", true},
		{"cmd -v foo", true},
		{"cmd foo", false},
		{"cmd --other", false},
		{"cmd", false},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			got := Evaluate(spec, defaultCtx(), parseWords(t, tt.cmd))
			if got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConditionHasFlagPattern(t *testing.T) {
	t.Run("ShortChars", func(t *testing.T) {
		spec := makeSpec([]string{"cmd"}, []*rulespb.Rule{allowRule(hasFlagPattern("iv"))})
		tests := []struct {
			cmd  string
			want bool
		}{
			{"cmd -i", true},
			{"cmd -v", true},
			{"cmd -x", false},
			{"cmd --inline", false}, // short_chars only matches single-char flags
			{"cmd", false},
		}
		for _, tt := range tests {
			t.Run(tt.cmd, func(t *testing.T) {
				got := Evaluate(spec, defaultCtx(), parseWords(t, tt.cmd))
				if got != tt.want {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			})
		}
	})

	t.Run("LongPrefixes", func(t *testing.T) {
		spec := makeSpec([]string{"cmd"}, []*rulespb.Rule{allowRule(hasFlagPattern("", "--in-place"))})
		tests := []struct {
			cmd  string
			want bool
		}{
			{"cmd --in-place", true},
			{"cmd --in-place-backup", true}, // prefix match
			{"cmd --other", false},
			{"cmd -i", false},
		}
		for _, tt := range tests {
			t.Run(tt.cmd, func(t *testing.T) {
				got := Evaluate(spec, defaultCtx(), parseWords(t, tt.cmd))
				if got != tt.want {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			})
		}
	})
}

// --------------------------------------------------------------------------- Condition: FlagArgCheck
// ---------------------------------------------------------------------------

func TestConditionFlagArgCheck(t *testing.T) {
	// Allow always + deny if -o flag arg fails write check.
	spec := makeSpec([]string{"cmd"}, []*rulespb.Rule{
		allowRule(alwaysCond()),
		allowRule(flagArgCheckCond([]string{"-o"}, true, writeCheck())),
	})

	tests := []struct {
		name string
		cmd  string
		want bool
	}{
		{"flag absent is vacuously true", "cmd foo", true},
		{"flag arg passes check", "cmd -o /allowed/out", true},
		{"flag arg fails check", "cmd -o /forbidden/out", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Evaluate(spec, defaultCtx(), parseWords(t, tt.cmd))
			if got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// --------------------------------------------------------------------------- Condition: EveryFlagMatches
// ---------------------------------------------------------------------------

func TestConditionEveryFlagMatches(t *testing.T) {
	spec := makeSpec([]string{"cmd"}, []*rulespb.Rule{
		allowRule(everyFlagMatchesCond([]string{"-v", "--verbose"}, []string{"-o"})),
	})

	tests := []struct {
		name string
		cmd  string
		want bool
	}{
		{"no flags", "cmd", true},
		{"known no-arg flag", "cmd -v", true},
		{"known flag with arg", "cmd -o out.txt", true},
		{"multiple known flags", "cmd -v -o out.txt --verbose", true},
		{"unknown flag rejected", "cmd -v --unknown", false},
		{"unknown flag only", "cmd --foo", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Evaluate(spec, defaultCtx(), parseWords(t, tt.cmd))
			if got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// --------------------------------------------------------------------------- Condition: EveryPositionalPasses
// ---------------------------------------------------------------------------

func TestConditionEveryPositionalPasses(t *testing.T) {
	spec := makeSpec([]string{"cmd"}, []*rulespb.Rule{
		allowRule(everyPositionalPassesCond(writeCheck())),
	})

	tests := []struct {
		name string
		cmd  string
		want bool
	}{
		{"no positionals vacuously true", "cmd", true},
		{"all pass", "cmd /allowed/a /allowed/b", true},
		{"one fails", "cmd /allowed/a /forbidden/b", false},
		{"all fail", "cmd /forbidden/a /forbidden/b", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Evaluate(spec, defaultCtx(), parseWords(t, tt.cmd))
			if got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// --------------------------------------------------------------------------- Condition: SubcommandCheck
// ---------------------------------------------------------------------------

func TestConditionSubcommandCheck(t *testing.T) {
	t.Run("allowed_with_any_args", func(t *testing.T) {
		spec := makeSpec([]string{"git"}, []*rulespb.Rule{
			allowRule(subcommandCond([]string{"status", "log"})),
		})
		tests := []struct {
			cmd  string
			want bool
		}{
			{"git status", true},
			{"git log --oneline", true},
			{"git push", false},
			{"git", false}, // no subcommand
		}
		for _, tt := range tests {
			t.Run(tt.cmd, func(t *testing.T) {
				got := Evaluate(spec, defaultCtx(), parseWords(t, tt.cmd))
				if got != tt.want {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			})
		}
	})

	t.Run("with_rules custom_rules override", func(t *testing.T) {
		// "add" subcommand only allowed with positionals passing write check.
		entry := rulespb.SubcommandEntry_builder{
			Names: []string{"add"},
			CustomRules: rulespb.CustomRules_builder{
				Rules: []*rulespb.Rule{
					allowRule(everyPositionalPassesCond(writeCheck())),
				},
			}.Build(),
		}.Build()
		spec := makeSpec([]string{"git"}, []*rulespb.Rule{
			allowRule(subcommandCond(nil, entry)),
		})

		tests := []struct {
			name string
			cmd  string
			want bool
		}{
			{"add allowed path", "git add /allowed/file", true},
			{"add forbidden path", "git add /forbidden/file", false},
			{"add no args vacuously true", "git add", true},
			{"unknown subcommand", "git push", false},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := Evaluate(spec, defaultCtx(), parseWords(t, tt.cmd))
				if got != tt.want {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			})
		}
	})

	t.Run("with_rules ref_command_spec", func(t *testing.T) {
		// The subcommand references another CommandSpec by name.
		refSpec := rulespb.CommandSpec_builder{
			Names: []string{"_ref_inner"},
			CustomRules: rulespb.CustomRules_builder{
				Rules: []*rulespb.Rule{allowRule(noArgsCond())},
			}.Build(),
		}.Build()
		refName := "_ref_inner"
		entry := rulespb.SubcommandEntry_builder{
			Names:          []string{"sub"},
			RefCommandSpec: &refName,
		}.Build()
		spec := makeSpec([]string{"cmd"}, []*rulespb.Rule{
			allowRule(subcommandCond(nil, entry)),
		})

		ctx := defaultCtx()
		ctx.RuleSet = rulespb.RuleSet_builder{Commands: []*rulespb.CommandSpec{refSpec}}.Build()

		if !Evaluate(spec, ctx, parseWords(t, "cmd sub")) {
			t.Fatal("expected allowed: sub with no remaining args matches NoArgs")
		}
		if Evaluate(spec, ctx, parseWords(t, "cmd sub extra")) {
			t.Fatal("expected denied: sub with extra arg fails NoArgs")
		}
	})

	t.Run("ref_command_spec missing rule set", func(t *testing.T) {
		refName := "_missing"
		entry := rulespb.SubcommandEntry_builder{
			Names:          []string{"sub"},
			RefCommandSpec: &refName,
		}.Build()
		spec := makeSpec([]string{"cmd"}, []*rulespb.Rule{
			allowRule(subcommandCond(nil, entry)),
		})
		ctx := defaultCtx()
		ctx.RuleSet = nil
		if Evaluate(spec, ctx, parseWords(t, "cmd sub")) {
			t.Fatal("expected denied with nil RuleSet")
		}
	})
}

// --------------------------------------------------------------------------- Condition: MaxPositionals
// ---------------------------------------------------------------------------

func TestConditionMaxPositionals(t *testing.T) {
	spec := makeSpec([]string{"cmd"}, []*rulespb.Rule{
		allowRule(maxPositionalsCond(2)),
	})

	tests := []struct {
		cmd  string
		want bool
	}{
		{"cmd", true},
		{"cmd a", true},
		{"cmd a b", true},
		{"cmd a b c", false},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			got := Evaluate(spec, defaultCtx(), parseWords(t, tt.cmd))
			if got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// --------------------------------------------------------------------------- Parser: flag/positional classification
// ---------------------------------------------------------------------------

func TestParserBasics(t *testing.T) {
	t.Run("flags and positionals", func(t *testing.T) {
		flagDefs := map[string]flagDef{"-o": {hasArg: true}, "-v": {hasArg: false}}
		words := parseWords(t, "cmd -v -o out.txt file.txt")
		parsed, ok := parseArgs(words[1:], flagDefs, false)
		if !ok {
			t.Fatal("parseArgs failed")
		}
		if len(parsed.flags) != 2 {
			t.Fatalf("expected 2 flags, got %d", len(parsed.flags))
		}
		if parsed.flags[0].name != "-v" {
			t.Errorf("flag[0] name = %q, want -v", parsed.flags[0].name)
		}
		if parsed.flags[1].name != "-o" || parsed.flags[1].value != "out.txt" {
			t.Errorf("flag[1] = {%q, %q}, want {-o, out.txt}", parsed.flags[1].name, parsed.flags[1].value)
		}
		if len(parsed.positionals) != 1 || parsed.positionals[0] != "file.txt" {
			t.Errorf("positionals = %v, want [file.txt]", parsed.positionals)
		}
	})

	t.Run("--flag=value splitting", func(t *testing.T) {
		flagDefs := map[string]flagDef{"--output": {hasArg: true}}
		words := parseWords(t, "cmd --output=result.txt")
		parsed, ok := parseArgs(words[1:], flagDefs, false)
		if !ok {
			t.Fatal("parseArgs failed")
		}
		if len(parsed.flags) != 1 {
			t.Fatalf("expected 1 flag, got %d", len(parsed.flags))
		}
		if parsed.flags[0].name != "--output" || parsed.flags[0].value != "result.txt" {
			t.Errorf("flag = {%q, %q}, want {--output, result.txt}", parsed.flags[0].name, parsed.flags[0].value)
		}
	})

	t.Run("--flag=value unknown flag stored as-is", func(t *testing.T) {
		flagDefs := map[string]flagDef{}
		words := parseWords(t, "cmd --unknown=val")
		parsed, ok := parseArgs(words[1:], flagDefs, false)
		if !ok {
			t.Fatal("parseArgs failed")
		}
		if len(parsed.flags) != 1 {
			t.Fatalf("expected 1 flag, got %d", len(parsed.flags))
		}
		// Unknown --flag=value is stored with full token as name.
		if parsed.flags[0].name != "--unknown=val" {
			t.Errorf("flag name = %q, want --unknown=val", parsed.flags[0].name)
		}
	})

	t.Run("double dash handling", func(t *testing.T) {
		flagDefs := map[string]flagDef{"-v": {hasArg: false}}
		words := parseWords(t, "cmd -v -- -v foo")
		parsed, ok := parseArgs(words[1:], flagDefs, false)
		if !ok {
			t.Fatal("parseArgs failed")
		}
		if !parsed.hasDoubleDash {
			t.Error("expected hasDoubleDash = true")
		}
		// -v before -- is a flag, -v after -- is a positional.
		if len(parsed.flags) != 1 {
			t.Fatalf("expected 1 flag, got %d", len(parsed.flags))
		}
		if len(parsed.positionals) != 2 {
			t.Fatalf("expected 2 positionals, got %d: %v", len(parsed.positionals), parsed.positionals)
		}
		if parsed.positionals[0] != "-v" || parsed.positionals[1] != "foo" {
			t.Errorf("positionals = %v, want [-v foo]", parsed.positionals)
		}
	})

	t.Run("flag arg consumption", func(t *testing.T) {
		flagDefs := map[string]flagDef{"-o": {hasArg: true}}
		words := parseWords(t, "cmd -o output")
		parsed, ok := parseArgs(words[1:], flagDefs, false)
		if !ok {
			t.Fatal("parseArgs failed")
		}
		if len(parsed.flags) != 1 {
			t.Fatalf("expected 1 flag, got %d", len(parsed.flags))
		}
		if parsed.flags[0].value != "output" {
			t.Errorf("flag value = %q, want output", parsed.flags[0].value)
		}
		if len(parsed.positionals) != 0 {
			t.Errorf("expected 0 positionals, got %v", parsed.positionals)
		}
	})

	t.Run("flags after positionals treated as flags", func(t *testing.T) {
		flagDefs := map[string]flagDef{"-v": {hasArg: false}}
		words := parseWords(t, "cmd foo -v")
		parsed, ok := parseArgs(words[1:], flagDefs, false)
		if !ok {
			t.Fatal("parseArgs failed")
		}
		if len(parsed.flags) != 1 || parsed.flags[0].name != "-v" {
			t.Errorf("expected -v flag, got %v", parsed.flags)
		}
		if len(parsed.positionals) != 1 || parsed.positionals[0] != "foo" {
			t.Errorf("expected [foo], got %v", parsed.positionals)
		}
	})
}

// --------------------------------------------------------------------------- Parser: combined short flags
// ---------------------------------------------------------------------------

func TestParserCombinedShortFlags(t *testing.T) {
	t.Run("basic decomposition", func(t *testing.T) {
		flagDefs := map[string]flagDef{"-a": {}, "-b": {}, "-c": {}}
		words := parseWords(t, "cmd -abc")
		parsed, ok := parseArgs(words[1:], flagDefs, true)
		if !ok {
			t.Fatal("parseArgs failed")
		}
		if len(parsed.flags) != 3 {
			t.Fatalf("expected 3 flags, got %d", len(parsed.flags))
		}
		for i, expected := range []string{"-a", "-b", "-c"} {
			if parsed.flags[i].name != expected {
				t.Errorf("flag[%d] = %q, want %q", i, parsed.flags[i].name, expected)
			}
		}
	})

	t.Run("last char consumes arg", func(t *testing.T) {
		flagDefs := map[string]flagDef{"-a": {}, "-b": {}, "-o": {hasArg: true}}
		words := parseWords(t, "cmd -abo out.txt")
		parsed, ok := parseArgs(words[1:], flagDefs, true)
		if !ok {
			t.Fatal("parseArgs failed")
		}
		if len(parsed.flags) != 3 {
			t.Fatalf("expected 3 flags, got %d", len(parsed.flags))
		}
		if parsed.flags[2].name != "-o" || parsed.flags[2].value != "out.txt" {
			t.Errorf("last flag = {%q, %q}, want {-o, out.txt}", parsed.flags[2].name, parsed.flags[2].value)
		}
	})

	t.Run("unknown char falls through to exact match", func(t *testing.T) {
		// -xyz where x is unknown — decomposition fails, tries as exact flag.
		flagDefs := map[string]flagDef{"-a": {}}
		words := parseWords(t, "cmd -xyz")
		parsed, ok := parseArgs(words[1:], flagDefs, true)
		if !ok {
			t.Fatal("parseArgs failed")
		}
		// Falls through to bare flag since -xyz is not a known exact flag either.
		if len(parsed.flags) != 1 || parsed.flags[0].name != "-xyz" {
			t.Errorf("expected [-xyz], got %v", parsed.flags)
		}
	})

	t.Run("disabled combined flags", func(t *testing.T) {
		flagDefs := map[string]flagDef{"-a": {}, "-b": {}, "-c": {}}
		words := parseWords(t, "cmd -abc")
		parsed, ok := parseArgs(words[1:], flagDefs, false)
		if !ok {
			t.Fatal("parseArgs failed")
		}
		// Without combined flag support, -abc is treated as a single flag.
		if len(parsed.flags) != 1 || parsed.flags[0].name != "-abc" {
			t.Errorf("expected single flag -abc, got %v", parsed.flags)
		}
	})
}

// --------------------------------------------------------------------------- Edge cases
// ---------------------------------------------------------------------------

func TestEdgeCases(t *testing.T) {
	t.Run("non-literal args fail parsing for non-always rules", func(t *testing.T) {
		// Non-always rules require parsing, which fails on non-literal args.
		spec := makeSpec([]string{"cmd"}, []*rulespb.Rule{
			allowRule(noArgsCond()),
		})
		words := parseWords(t, "cmd $VAR")
		got := Evaluate(spec, defaultCtx(), words)
		if got {
			t.Fatal("expected denied for non-literal arg with non-always rule")
		}
	})

	t.Run("non-literal args allowed for always-allow", func(t *testing.T) {
		// always{} fast path skips parsing, allowing non-literal args.
		spec := makeSpec([]string{"cmd"}, []*rulespb.Rule{allowRule(alwaysCond())})
		words := parseWords(t, "cmd $VAR")
		got := Evaluate(spec, defaultCtx(), words)
		if !got {
			t.Fatal("expected allowed for non-literal arg with always rule")
		}
	})

	t.Run("empty command", func(t *testing.T) {
		words := parseWords(t, "")
		if words != nil {
			t.Skip("shell parser returned words for empty input")
		}
	})

	t.Run("deny with pattern flags", func(t *testing.T) {
		// Deny any -i flag (edit in place pattern).
		spec := makeSpec([]string{"cmd"}, []*rulespb.Rule{
			allowRule(alwaysCond()),
			denyRule(hasFlagPattern("i")),
		})
		if Evaluate(spec, defaultCtx(), parseWords(t, "cmd -i")) {
			t.Fatal("expected denied for -i flag")
		}
		if !Evaluate(spec, defaultCtx(), parseWords(t, "cmd -v")) {
			t.Fatal("expected allowed for -v flag")
		}
	})

	t.Run("nil condition treated as true", func(t *testing.T) {
		// A rule with nil condition: evalCondition returns true for nil.
		spec := makeSpec([]string{"cmd"}, []*rulespb.Rule{allowRule(nil)})
		if !Evaluate(spec, defaultCtx(), parseWords(t, "cmd")) {
			t.Fatal("expected allowed for nil condition")
		}
	})

	t.Run("combined deny and allow interaction", func(t *testing.T) {
		// Allow always, deny if has double dash → blocks "cmd -- foo".
		spec := makeSpec([]string{"cmd"}, []*rulespb.Rule{
			allowRule(alwaysCond()),
			denyRule(hasDoubleDashCond()),
		})
		if Evaluate(spec, defaultCtx(), parseWords(t, "cmd -- foo")) {
			t.Fatal("expected denied")
		}
		if !Evaluate(spec, defaultCtx(), parseWords(t, "cmd foo")) {
			t.Fatal("expected allowed")
		}
	})

	t.Run("max positionals with flags", func(t *testing.T) {
		// Flags should not count toward positional limit.
		spec := makeSpec([]string{"cmd"}, []*rulespb.Rule{
			allowRule(maxPositionalsCond(1)),
			allowRule(everyFlagMatchesCond([]string{"-v"}, nil)),
		})
		if !Evaluate(spec, defaultCtx(), parseWords(t, "cmd -v foo")) {
			t.Fatal("expected allowed: 1 positional, 1 flag")
		}
		if Evaluate(spec, defaultCtx(), parseWords(t, "cmd -v foo bar")) {
			t.Fatal("expected denied: 2 positionals")
		}
	})
}

func TestDefaultRulesLoad(t *testing.T) {
	rs, err := DefaultRules()
	if err != nil {
		t.Fatalf("DefaultRules() error: %v", err)
	}
	if len(rs.GetCommands()) == 0 {
		t.Fatal("DefaultRules() returned empty rule set")
	}
	for _, name := range []string{"cat", "git", "ssh", "find", "sed"} {
		if LookupCommand(rs, name) == nil {
			t.Errorf("LookupCommand(%q) = nil, want non-nil", name)
		}
	}
}

// --------------------------------------------------------------------------- Check: RecurseEval
// ---------------------------------------------------------------------------

func TestRecurseEval(t *testing.T) {
	// Track what command string gets passed to recursive Evaluate.
	var lastEvalCmd string
	evalCtx := func(allow bool) *EvalCtx {
		return &EvalCtx{
			Cwd:           "/project",
			IsPathAllowed: func(string) bool { return true },
			Evaluate: func(cmd, cwd string, dirs []string) bool {
				lastEvalCmd = cmd
				return allow
			},
		}
	}

	t.Run("basic recurse", func(t *testing.T) {
		// Wrapper with no flag defs: all args are positionals + raw flags.
		spec := makeSpec([]string{"wrapper"}, []*rulespb.Rule{
			alwaysAllowRule(),
			denyRule(notCond(everyPositionalPassesCond(recurseEvalCheck()))),
		})
		ctx := evalCtx(true)
		if !Evaluate(spec, ctx, parseWords(t, "wrapper ls -la")) {
			t.Fatal("expected allowed")
		}
		if lastEvalCmd != "ls -la" {
			t.Fatalf("recursive eval got %q, want %q", lastEvalCmd, "ls -la")
		}
	})

	t.Run("skips consumed flags before first positional", func(t *testing.T) {
		// Wrapper with -u <user> flag: flag+arg should be stripped from the recursive command. Define -u as consuming an arg
		// so the parser strips it.
		spec := makeSpec([]string{"sudo-wrapper"}, []*rulespb.Rule{
			alwaysAllowRule(),
			denyRule(notCond(everyPositionalPassesCond(recurseEvalCheck()))),
			allowRule(flagArgCheckCond([]string{"-u"}, true, nil)),
		})

		ctx := evalCtx(true)
		if !Evaluate(spec, ctx, parseWords(t, "sudo-wrapper -u root ls -la /tmp")) {
			t.Fatal("expected allowed")
		}
		if lastEvalCmd != "ls -la /tmp" {
			t.Fatalf("recursive eval got %q, want %q", lastEvalCmd, "ls -la /tmp")
		}
	})

	t.Run("long flag with equals skipped", func(t *testing.T) {
		spec := makeSpec([]string{"sudo-wrapper"}, []*rulespb.Rule{
			alwaysAllowRule(),
			denyRule(notCond(everyPositionalPassesCond(recurseEvalCheck()))),
			allowRule(flagArgCheckCond([]string{"--user"}, true, nil)),
		})

		ctx := evalCtx(true)
		if !Evaluate(spec, ctx, parseWords(t, "sudo-wrapper --user=root cat /etc/hosts")) {
			t.Fatal("expected allowed")
		}
		if lastEvalCmd != "cat /etc/hosts" {
			t.Fatalf("recursive eval got %q, want %q", lastEvalCmd, "cat /etc/hosts")
		}
	})

	t.Run("recursive eval denies unsafe command", func(t *testing.T) {
		spec := makeSpec([]string{"wrapper"}, []*rulespb.Rule{
			alwaysAllowRule(),
			denyRule(notCond(everyPositionalPassesCond(recurseEvalCheck()))),
		})
		ctx := evalCtx(false)
		if Evaluate(spec, ctx, parseWords(t, "wrapper some-dangerous-cmd")) {
			t.Fatal("expected denied when recursive eval rejects")
		}
	})

	t.Run("no positionals vacuously true", func(t *testing.T) {
		spec := makeSpec([]string{"wrapper"}, []*rulespb.Rule{
			alwaysAllowRule(),
			denyRule(notCond(everyPositionalPassesCond(recurseEvalCheck()))),
		})
		ctx := evalCtx(false) // wouldn't matter, shouldn't be called
		lastEvalCmd = ""
		if !Evaluate(spec, ctx, parseWords(t, "wrapper --help")) {
			t.Fatal("expected allowed: flag-only invocation, no positionals")
		}
		if lastEvalCmd != "" {
			t.Fatal("Evaluate should not have been called for flag-only invocation")
		}
	})

	t.Run("double dash separator", func(t *testing.T) {
		spec := makeSpec([]string{"wrapper"}, []*rulespb.Rule{
			alwaysAllowRule(),
			denyRule(notCond(everyPositionalPassesCond(recurseEvalCheck()))),
		})
		ctx := evalCtx(true)
		if !Evaluate(spec, ctx, parseWords(t, "wrapper -- ls -la")) {
			t.Fatal("expected allowed")
		}
		if lastEvalCmd != "ls -la" {
			t.Fatalf("recursive eval got %q, want %q", lastEvalCmd, "ls -la")
		}
	})

	t.Run("double dash with consumed flags before it", func(t *testing.T) {
		spec := makeSpec([]string{"sudo-wrapper"}, []*rulespb.Rule{
			alwaysAllowRule(),
			denyRule(notCond(everyPositionalPassesCond(recurseEvalCheck()))),
			allowRule(flagArgCheckCond([]string{"-u"}, true, nil)),
		})
		ctx := evalCtx(true)
		if !Evaluate(spec, ctx, parseWords(t, "sudo-wrapper -u root -- ls -la")) {
			t.Fatal("expected allowed")
		}
		if lastEvalCmd != "ls -la" {
			t.Fatalf("recursive eval got %q, want %q", lastEvalCmd, "ls -la")
		}
	})
}
