package rules

import (
	rulespb "claude-auto-permission/internal/gen/rules/v1"
	"claude-auto-permission/internal/staticbash/cmdcheck"

	"mvdan.cc/sh/v3/syntax"
)

// runOpaqueChecker resolves an opaque-checker reference (sed/awk in the proto) and runs it. Missing Registry or
// unregistered name → not allowed.
func runOpaqueChecker(ctx *EvalCtx, name string, args []*syntax.Word) bool {
	if ctx.Checkers == nil {
		return false
	}
	c, ok := ctx.Checkers.Lookup(name)
	if !ok {
		return false
	}
	return c.Check(&cmdcheck.Context{
		Cwd:           ctx.Cwd,
		WriteDirs:     ctx.WriteDirs,
		IsPathAllowed: ctx.IsPathAllowed,
		Evaluate:      ctx.Evaluate,
	}, args)
}

// Evaluate checks whether a command invocation is safe under spec.
func Evaluate(spec *rulespb.CommandSpec, ctx *EvalCtx, args []*syntax.Word) bool {
	switch spec.WhichChecker() {
	case rulespb.CommandSpec_CustomRules_case:
		cr := spec.GetCustomRules()
		// Skip parsing for the trivial allow{always{}} case.
		if isAlwaysAllow(cr) {
			if cr.GetRequireCwd() && ctx.Cwd == "" {
				return false
			}
			return true
		}
		parsed, ok := parseCommand(cr, args)
		if !ok {
			return false
		}
		return evaluateCustomRules(cr, parsed, ctx)

	case rulespb.CommandSpec_SedChecker_case:
		return runOpaqueChecker(ctx, "sed", args)

	case rulespb.CommandSpec_AwkChecker_case:
		return runOpaqueChecker(ctx, "awk", args)

	default:
		return false
	}
}

// evaluateCustomRules runs the allow/deny rule logic against parsed args.
func evaluateCustomRules(cr *rulespb.CustomRules, parsed *parsedArgs, ctx *EvalCtx) bool {
	if len(cr.GetRules()) == 0 {
		return false
	}
	if cr.GetRequireCwd() && ctx.Cwd == "" {
		return false
	}

	hasAllow := false
	for _, rule := range cr.GetRules() {
		switch rule.WhichAction() {
		case rulespb.Rule_Allow_case:
			if !evalCondition(rule.GetAllow().GetCondition(), parsed, ctx) {
				return false
			}
			hasAllow = true
		case rulespb.Rule_Deny_case:
			if evalCondition(rule.GetDeny().GetCondition(), parsed, ctx) {
				return false
			}
		case rulespb.Rule_AlwaysAllow_case:
			hasAllow = true
		case rulespb.Rule_AlwaysDeny_case:
			return false
		}
	}
	return hasAllow
}

// isAlwaysAllow returns true for a CustomRules that is just a single allow { always {} } — safe with any args.
func isAlwaysAllow(cr *rulespb.CustomRules) bool {
	if len(cr.GetRules()) != 1 {
		return false
	}
	rule := cr.GetRules()[0]
	switch rule.WhichAction() {
	case rulespb.Rule_AlwaysAllow_case:
		return true
	case rulespb.Rule_Allow_case:
		return rule.GetAllow().GetCondition().WhichCondition() == rulespb.Condition_Always_case
	default:
		return false
	}
}

// parseCommand parses shell words using flag defs extracted from the CustomRules conditions.
func parseCommand(cr *rulespb.CustomRules, args []*syntax.Word) (*parsedArgs, bool) {
	flagDefs := collectFlagDefs(cr)
	return parseArgs(args[1:], flagDefs, cr.GetSupportCombinedShortFlags())
}

// collectFlagDefs gathers flag defs across all conditions so the parser knows which flags consume an argument.
func collectFlagDefs(cr *rulespb.CustomRules) map[string]flagDef {
	defs := make(map[string]flagDef)
	for _, rule := range cr.GetRules() {
		var cond *rulespb.Condition
		switch rule.WhichAction() {
		case rulespb.Rule_Allow_case:
			cond = rule.GetAllow().GetCondition()
		case rulespb.Rule_Deny_case:
			cond = rule.GetDeny().GetCondition()
		}
		collectFlagDefsFromCondition(cond, defs)
	}
	return defs
}

func collectFlagDefsFromCondition(cond *rulespb.Condition, defs map[string]flagDef) {
	if cond == nil {
		return
	}
	switch cond.WhichCondition() {
	case rulespb.Condition_Not_case:
		collectFlagDefsFromCondition(cond.GetNot().GetCondition(), defs)

	case rulespb.Condition_FlagArgCheck_case:
		fac := cond.GetFlagArgCheck()
		for _, name := range fac.GetNames() {
			defs[name] = flagDef{hasArg: fac.GetHasArg()}
		}

	case rulespb.Condition_EveryFlagMatches_case:
		efm := cond.GetEveryFlagMatches()
		for _, name := range efm.GetAllowedWithoutArgs() {
			defs[name] = flagDef{hasArg: false}
		}
		for _, name := range efm.GetAllowedWithArgs() {
			defs[name] = flagDef{hasArg: true}
		}

	case rulespb.Condition_Subcommands_case:
		// Subcommand overrides need their flag defs collected too — the parser runs once over the whole command before
		// subcommand dispatch.
		for _, entry := range cond.GetSubcommands().GetWithRules() {
			if entry.WhichRules() != rulespb.SubcommandEntry_CustomRules_case {
				continue
			}
			cr := entry.GetCustomRules()
			for _, rule := range cr.GetRules() {
				var sub *rulespb.Condition
				switch rule.WhichAction() {
				case rulespb.Rule_Allow_case:
					sub = rule.GetAllow().GetCondition()
				case rulespb.Rule_Deny_case:
					sub = rule.GetDeny().GetCondition()
				}
				collectFlagDefsFromCondition(sub, defs)
			}
		}
	}
}
