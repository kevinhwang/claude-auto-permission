package cmdcheck

import (
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// LiteralString extracts a plain string from a Word that contains only literal parts (Lit, SglQuoted, or DblQuoted with
// only Lit inside). Returns ("", false) if the word contains any expansions or substitutions.
func LiteralString(word *syntax.Word) (string, bool) {
	if word == nil {
		return "", false
	}
	var b strings.Builder
	for _, part := range word.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			b.WriteString(p.Value)
		case *syntax.SglQuoted:
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			for _, dp := range p.Parts {
				lit, ok := dp.(*syntax.Lit)
				if !ok {
					return "", false
				}
				b.WriteString(lit.Value)
			}
		default:
			return "", false
		}
	}
	if b.Len() == 0 {
		return "", false
	}
	return b.String(), true
}

// CommandName extracts the command name from the first word of a CallExpr. Returns the basename (e.g. /usr/bin/git →
// git).
func CommandName(word *syntax.Word) (string, bool) {
	s, ok := LiteralString(word)
	if !ok {
		return "", false
	}
	return filepath.Base(s), true
}

// LiteralArgs extracts string values from a slice of Words. Returns (nil, false) if any word contains expansions.
func LiteralArgs(words []*syntax.Word) ([]string, bool) {
	result := make([]string, 0, len(words))
	for _, w := range words {
		s, ok := LiteralString(w)
		if !ok {
			return nil, false
		}
		result = append(result, s)
	}
	return result, true
}
