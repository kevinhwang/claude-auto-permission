package cmdcheck

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// ToSet creates a map[string]bool from a list of strings.
func ToSet(items ...string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, item := range items {
		m[item] = true
	}
	return m
}

// HasWriteFlags checks if args contain flags that make an otherwise-safe command dangerous (e.g. sed -i, sort -o).
func HasWriteFlags(args []*syntax.Word, shortChars string, longPrefixes ...string) bool {
	for _, arg := range args[1:] {
		s, ok := LiteralString(arg)
		if !ok {
			continue
		}
		for _, lp := range longPrefixes {
			if strings.HasPrefix(s, lp) {
				return true
			}
		}
		if shortChars != "" && strings.HasPrefix(s, "-") && !strings.HasPrefix(s, "--") {
			for _, ch := range shortChars {
				if strings.ContainsRune(s[1:], ch) {
					return true
				}
			}
		}
	}
	return false
}
