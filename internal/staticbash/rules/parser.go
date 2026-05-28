package rules

import (
	"strings"

	"claude-auto-permission/internal/staticbash/cmdcheck"

	"mvdan.cc/sh/v3/syntax"
)

type flagDef struct {
	hasArg bool
}

// parsedArgs is the structured representation of a command's arguments.
type parsedArgs struct {
	flags                 []parsedFlag
	positionals           []string
	rawWords              []*syntax.Word
	hasDoubleDash         bool
	positionalWordIndices []int // raw word index per positional
}

type parsedFlag struct {
	name  string
	value string
}

// parseArgs classifies shell words into flags and positionals. supportCombined enables `-abc` decomposition into
// `-a -b -c`. Words must resolve to literals — variable expansion isn't supported.
func parseArgs(words []*syntax.Word, flagDefs map[string]flagDef, supportCombined bool) (*parsedArgs, bool) {
	result := &parsedArgs{rawWords: words}
	pastDoubleDash := false

	for i := 0; i < len(words); i++ {
		s, ok := cmdcheck.LiteralString(words[i])
		if !ok {
			return nil, false
		}

		if !pastDoubleDash && s == "--" {
			result.hasDoubleDash = true
			pastDoubleDash = true
			continue
		}

		if !pastDoubleDash && isFlag(s) {
			if eqIdx := strings.Index(s, "="); eqIdx > 0 && strings.HasPrefix(s, "--") {
				flagName := s[:eqIdx]
				flagVal := s[eqIdx+1:]
				if def, ok := flagDefs[flagName]; ok && def.hasArg {
					result.flags = append(result.flags, parsedFlag{name: flagName, value: flagVal})
					continue
				}
				// Unknown --flag=value: store verbatim so EveryFlagMatches can reject.
				result.flags = append(result.flags, parsedFlag{name: s})
				continue
			}

			if supportCombined && len(s) > 2 && s[0] == '-' && s[1] != '-' {
				if decomposed, ok := decomposeCombinedFlags(s, flagDefs, words, &i); ok {
					result.flags = append(result.flags, decomposed...)
					continue
				}
				// Decomposition failed; fall through to exact-flag handling.
			}

			if def, ok := flagDefs[s]; ok && def.hasArg {
				var val string
				if i+1 < len(words) {
					i++
					val, ok = cmdcheck.LiteralString(words[i])
					if !ok {
						return nil, false
					}
				}
				result.flags = append(result.flags, parsedFlag{name: s, value: val})
				continue
			}

			result.flags = append(result.flags, parsedFlag{name: s})
			continue
		}

		result.positionalWordIndices = append(result.positionalWordIndices, i)
		result.positionals = append(result.positionals, s)
	}

	return result, true
}

// decomposeCombinedFlags splits -abc into individual flags. Returns false on any unknown char. The last char may
// consume the next word if its def has hasArg.
func decomposeCombinedFlags(s string, flagDefs map[string]flagDef, words []*syntax.Word, idx *int) ([]parsedFlag, bool) {
	chars := s[1:]
	var flags []parsedFlag

	for j, ch := range chars {
		name := "-" + string(ch)
		def, known := flagDefs[name]
		if !known {
			return nil, false
		}
		isLast := j == len([]rune(chars))-1
		if def.hasArg && !isLast {
			return nil, false
		}
		if def.hasArg && isLast {
			var val string
			if *idx+1 < len(words) {
				*idx++
				var ok bool
				val, ok = cmdcheck.LiteralString(words[*idx])
				if !ok {
					return nil, false
				}
			}
			flags = append(flags, parsedFlag{name: name, value: val})
		} else {
			flags = append(flags, parsedFlag{name: name})
		}
	}
	return flags, true
}

func isFlag(s string) bool {
	return len(s) > 1 && s[0] == '-'
}
