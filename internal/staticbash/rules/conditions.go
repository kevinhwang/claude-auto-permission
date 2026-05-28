package rules

import (
	"slices"
	"strings"

	rulespb "claude-auto-permission/internal/gen/rules/v1"
	"claude-auto-permission/internal/staticbash/cmdcheck"

	"mvdan.cc/sh/v3/syntax"
)

// RemoteHostConfig is the slice of remote-host config the rule engine reads — just the writable-paths list. Mirrors
// `configpb.RemoteHost.GetAllowWritePatterns()` without coupling the rules package to the config proto.
type RemoteHostConfig interface {
	GetAllowWritePatterns() []string
}

// RemoteHostLookup is the per-cwd seam the SshRemoteEval condition uses. Returns the host's writable scope, or
// `(nil, false)` when the host isn't covered by any project's `remote_hosts`.
type RemoteHostLookup func(host, cwd string) (RemoteHostConfig, bool)

// EvalCtx is the evaluation environment for one condition check.
//
// Function-typed fields ([IsPathAllowed], [Evaluate], [RemoteHostLookup]) are the rules engine's seams onto its
// surroundings — the AST walker supplies real implementations; tests pass closures. Keeping them as function values
// means the rules package has no compile-time dependency on the walker, the project config, or any other domain glue.
type EvalCtx struct {
	Cwd       string
	WriteDirs []string

	IsPathAllowed    func(path string) bool
	Evaluate         func(command, cwd string, writeDirs []string) bool
	RemoteHostLookup RemoteHostLookup

	// RuleSet resolves ref_command_spec references.
	RuleSet *rulespb.RuleSet

	// Checkers resolves sed_checker/awk_checker references. Nil disables those rule types.
	Checkers *cmdcheck.Registry
}

// evalCondition evaluates a single condition against parsed args.
func evalCondition(cond *rulespb.Condition, parsed *parsedArgs, ctx *EvalCtx) bool {
	if cond == nil {
		return true
	}
	switch cond.WhichCondition() {
	case rulespb.Condition_Always_case:
		return true

	case rulespb.Condition_Not_case:
		return !evalCondition(cond.GetNot().GetCondition(), parsed, ctx)

	case rulespb.Condition_NoArgs_case:
		return len(parsed.flags) == 0 && len(parsed.positionals) == 0

	case rulespb.Condition_HasDoubleDash_case:
		return parsed.hasDoubleDash

	case rulespb.Condition_HasFlagMatching_case:
		return evalHasFlagMatching(cond.GetHasFlagMatching(), parsed)

	case rulespb.Condition_FlagArgCheck_case:
		return evalFlagArgCheck(cond.GetFlagArgCheck(), parsed, ctx)

	case rulespb.Condition_EveryFlagMatches_case:
		return evalEveryFlagMatches(cond.GetEveryFlagMatches(), parsed)

	case rulespb.Condition_EveryPositionalPasses_case:
		return evalEveryPositionalPasses(cond.GetEveryPositionalPasses(), parsed, ctx)

	case rulespb.Condition_Subcommands_case:
		return evalSubcommandCheck(cond.GetSubcommands(), parsed, ctx)

	case rulespb.Condition_MaxPositionals_case:
		return int32(len(parsed.positionals)) <= cond.GetMaxPositionals().GetCount()

	default:
		return false
	}
}

func evalHasFlagMatching(hfm *rulespb.HasFlagMatching, parsed *parsedArgs) bool {
	switch hfm.WhichMatch() {
	case rulespb.HasFlagMatching_Exact_case:
		exact := hfm.GetExact()
		set := toSet(exact.GetFlags())
		for _, f := range parsed.flags {
			if set[f.name] {
				return true
			}
			// --flag=value tokens that the parser didn't split (flag not in flagDefs) appear as f.name="--flag=value".
			for _, exactFlag := range exact.GetFlags() {
				if strings.HasPrefix(f.name, exactFlag+"=") {
					return true
				}
			}
		}
		return false

	case rulespb.HasFlagMatching_Pattern_case:
		return matchesPattern(hfm.GetPattern(), parsed)

	default:
		return false
	}
}

func matchesPattern(pf *rulespb.PatternFlags, parsed *parsedArgs) bool {
	for _, f := range parsed.flags {
		if matchesFlagPattern(pf, f.name) {
			return true
		}
	}
	return false
}

func matchesFlagPattern(pf *rulespb.PatternFlags, flagName string) bool {
	if pf.GetShortChars() != "" && len(flagName) == 2 && flagName[0] == '-' && flagName[1] != '-' {
		if strings.ContainsRune(pf.GetShortChars(), rune(flagName[1])) {
			return true
		}
	}
	for _, prefix := range pf.GetLongPrefixes() {
		if strings.HasPrefix(flagName, prefix) {
			return true
		}
	}
	return false
}

func evalFlagArgCheck(fac *rulespb.FlagArgCheck, parsed *parsedArgs, ctx *EvalCtx) bool {
	nameSet := toSet(fac.GetNames())
	for _, f := range parsed.flags {
		if !nameSet[f.name] {
			continue
		}
		if !evalCheck(fac.GetCheck(), f.value, parsed, ctx) {
			return false
		}
	}
	// Vacuously true when the flag isn't present.
	return true
}

func evalEveryFlagMatches(efm *rulespb.EveryFlagMatches, parsed *parsedArgs) bool {
	noArgSet := toSet(efm.GetAllowedWithoutArgs())
	withArgSet := toSet(efm.GetAllowedWithArgs())
	for _, f := range parsed.flags {
		if noArgSet[f.name] || withArgSet[f.name] {
			continue
		}
		return false
	}
	return true
}

func evalEveryPositionalPasses(epp *rulespb.EveryPositionalPasses, parsed *parsedArgs, ctx *EvalCtx) bool {
	// SshRemoteEval consumes all positionals at once (first=host, rest=remote command). Evaluate once instead of
	// per-positional.
	if epp.GetCheck().WhichCheck() == rulespb.Check_SshRemoteEval_case {
		if len(parsed.positionals) == 0 {
			return false
		}
		return evalSshRemoteEval(parsed.positionals[0], parsed, ctx)
	}
	for _, p := range parsed.positionals {
		if !evalCheck(epp.GetCheck(), p, parsed, ctx) {
			return false
		}
	}
	return true
}

func evalSubcommandCheck(sc *rulespb.SubcommandCheck, parsed *parsedArgs, ctx *EvalCtx) bool {
	if len(parsed.positionals) == 0 {
		return false
	}
	sub := parsed.positionals[0]

	if slices.Contains(sc.GetAllowedWithAnyArgs(), sub) {
		return true
	}

	for _, entry := range sc.GetWithRules() {
		if !matchesEntry(entry, sub) {
			continue
		}
		// Pass through the raw words after the subcommand so ref specs can re-parse with their own flag definitions.
		var remainingRawWords []*syntax.Word
		if len(parsed.positionalWordIndices) > 0 {
			subIdx := parsed.positionalWordIndices[0]
			if subIdx+1 < len(parsed.rawWords) {
				remainingRawWords = parsed.rawWords[subIdx+1:]
			}
		}
		remainingParsed := &parsedArgs{
			positionals: parsed.positionals[1:],
			rawWords:    remainingRawWords,
		}
		switch entry.WhichRules() {
		case rulespb.SubcommandEntry_CustomRules_case:
			return evaluateCustomRules(entry.GetCustomRules(), remainingParsed, ctx)
		case rulespb.SubcommandEntry_RefCommandSpec_case:
			return evaluateRefCommandSpec(entry.GetRefCommandSpec(), remainingParsed, ctx)
		}
		return false
	}

	return false
}

func matchesEntry(entry *rulespb.SubcommandEntry, sub string) bool {
	return slices.Contains(entry.GetNames(), sub)
}

func evaluateRefCommandSpec(refName string, parsed *parsedArgs, ctx *EvalCtx) bool {
	if ctx.RuleSet == nil {
		return false
	}
	spec := LookupCommand(ctx.RuleSet, refName)
	if spec == nil {
		return false
	}
	if spec.WhichChecker() != rulespb.CommandSpec_CustomRules_case {
		return false
	}
	cr := spec.GetCustomRules()
	// Re-parse with the referenced spec's flag defs — the outer parser didn't know about them.
	reparsed, ok := parseArgs(parsed.rawWords, collectFlagDefs(cr), cr.GetSupportCombinedShortFlags())
	if !ok {
		return false
	}
	return evaluateCustomRules(cr, reparsed, ctx)
}

func evalCheck(chk *rulespb.Check, target string, parsed *parsedArgs, ctx *EvalCtx) bool {
	if chk == nil {
		return true
	}
	switch chk.WhichCheck() {
	case rulespb.Check_WriteCheck_case:
		return ctx.IsPathAllowed(target)

	case rulespb.Check_SshRemoteEval_case:
		return evalSshRemoteEval(target, parsed, ctx)

	case rulespb.Check_RecurseEval_case:
		return evalRecurseEval(target, parsed, ctx)

	default:
		return false
	}
}

func evalSshRemoteEval(host string, parsed *parsedArgs, ctx *EvalCtx) bool {
	if ctx.RemoteHostLookup == nil {
		return false
	}
	hostCfg, matched := ctx.RemoteHostLookup(host, ctx.Cwd)
	if !matched {
		return false
	}

	// host is the first positional; the rest is the remote command.
	var remoteParts []string
	foundHost := false
	for _, p := range parsed.positionals {
		if !foundHost && p == host {
			foundHost = true
			continue
		}
		if foundHost {
			if len(remoteParts) == 0 && p == "--" {
				continue
			}
			remoteParts = append(remoteParts, p)
		}
	}

	remoteCmd := strings.Join(remoteParts, " ")
	if remoteCmd == "" {
		return false
	}

	return ctx.Evaluate(remoteCmd, "", hostCfg.GetAllowWritePatterns())
}

func evalRecurseEval(_ string, parsed *parsedArgs, ctx *EvalCtx) bool {
	// Start from the first positional, skipping wrapper flags like -u.
	words := parsed.rawWords
	if len(parsed.positionalWordIndices) > 0 {
		words = parsed.rawWords[parsed.positionalWordIndices[0]:]
	}
	remaining, ok := cmdcheck.LiteralArgs(words)
	if !ok {
		return false
	}
	cmd := strings.Join(remaining, " ")
	if cmd == "" {
		return false
	}
	return ctx.Evaluate(cmd, ctx.Cwd, ctx.WriteDirs)
}

func toSet(items []string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, item := range items {
		m[item] = true
	}
	return m
}
