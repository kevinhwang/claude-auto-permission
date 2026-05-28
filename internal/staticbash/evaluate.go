package staticbash

import (
	"encoding/json"
	"strings"

	"claude-auto-permission/internal/config"
	configpb "claude-auto-permission/internal/gen/config/v1"
	rulespb "claude-auto-permission/internal/gen/rules/v1"
	"claude-auto-permission/internal/staticbash/ast"
	"claude-auto-permission/internal/staticbash/cmdcheck"
	"claude-auto-permission/internal/staticbash/rules"
)

// Input is the structural judge's per-call input. It mirrors the fields a Bash hook event carries — the raw
// `tool_input` JSON plus the cwd / project-root context used for write-allow checks.
type Input struct {
	ToolInput   json.RawMessage
	Cwd         string
	ProjectRoot string
}

// Evaluate is the structural judge's public verdict function. It extracts the Bash command from `in`, parses + walks
// the AST, and returns the resulting [ast.Verdict].
//
// All failure modes (malformed JSON, parse error, empty command, no matching rule) collapse into a zero `Verdict` —
// caller decides how to describe the silence.
func Evaluate(in Input, checkers *cmdcheck.Registry, cfg *config.Resolver) ast.Verdict {
	var ti struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(in.ToolInput, &ti); err != nil {
		return ast.Verdict{}
	}
	return ast.Evaluate(ast.Input{
		Command:     strings.TrimSpace(ti.Command),
		Cwd:         in.Cwd,
		ProjectRoot: in.ProjectRoot,
		RuleSet:     resolveRules(cfg, in.ProjectRoot),
		Checkers:    checkers,
		Config:      cfg,
	})
}

// resolveRules merges the rule sets contributed by every project whose `path_patterns` match `projectRoot`. Projects
// without a `static_bash_rules` block contribute nothing.
//
// Selection per project:
//
//   - `use_default_rules{}` → the bundled default rule set.
//   - `custom_command_rules{ rule_set }` → the user-defined set.
//
// Returns nil when no project contributes rules; the AST walker then has nothing to match and reports `(false, nil)`.
func resolveRules(cfg *config.Resolver, projectRoot string) *rulespb.RuleSet {
	if cfg == nil {
		return nil
	}
	var sets []*rulespb.RuleSet
	for _, proj := range cfg.MatchingProjects(projectRoot) {
		sbr := proj.GetStaticBashRules()
		if sbr == nil {
			continue
		}
		switch sbr.WhichCommandRules() {
		case configpb.StaticBashRules_UseDefaultRules_case:
			defaults, err := rules.DefaultRules()
			if err != nil {
				continue
			}
			sets = append(sets, defaults)
		case configpb.StaticBashRules_CustomCommandRules_case:
			if ccr := sbr.GetCustomCommandRules(); ccr != nil && ccr.GetRuleSet() != nil {
				sets = append(sets, ccr.GetRuleSet())
			}
		}
	}
	if len(sets) == 0 {
		return nil
	}
	return rules.MergeRuleSets(sets...)
}
