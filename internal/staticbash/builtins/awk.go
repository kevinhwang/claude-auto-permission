package builtins

import (
	"strings"

	"claude-auto-permission/internal/staticbash/cmdcheck"

	"mvdan.cc/sh/v3/syntax"
)

type awkChecker struct{}

func (awkChecker) Check(_ *cmdcheck.Context, args []*syntax.Word) bool {
	i := 1
	for i < len(args) {
		s, ok := cmdcheck.LiteralString(args[i])
		if !ok {
			return false
		}
		if s == "-f" || strings.HasPrefix(s, "--file") {
			return false
		}
		if s == "-F" || s == "-v" {
			i += 2
			continue
		}
		if s == "-e" || s == "--source" {
			if i+1 >= len(args) {
				return false
			}
			prog, ok := cmdcheck.LiteralString(args[i+1])
			if !ok {
				return false
			}
			if awkProgramIsDangerous(prog) {
				return false
			}
			i += 2
			continue
		}
		if strings.HasPrefix(s, "-") {
			i++
			continue
		}
		if awkProgramIsDangerous(s) {
			return false
		}
		return true
	}
	return true
}

func awkProgramIsDangerous(prog string) bool {
	if strings.Contains(prog, "system") {
		return true
	}
	if strings.Contains(prog, "getline") {
		return true
	}
	// Detect output redirections (> / >> / |) without false-positive on comparisons (>= or `$3 > 100`) or regex
	// alternation (/a|b/). The heuristic: flag `>>` always; flag `>` or `|` when the next non-space char is a quote,
	// slash, or identifier (a redirect target).
	if strings.Contains(prog, ">>") {
		return true
	}
	for _, pat := range []string{`| "`, `|"`, `| '`, `|'`} {
		if strings.Contains(prog, pat) {
			return true
		}
	}
	n := len(prog)
	for i := 0; i < n; i++ {
		if prog[i] != '>' {
			continue
		}
		if i+1 < n && prog[i+1] == '=' {
			continue
		}
		j := i + 1
		for j < n && prog[j] == ' ' {
			j++
		}
		if j >= n {
			continue
		}
		next := prog[j]
		if next == '"' || next == '\'' || next == '/' || isAwkIdentStart(next) {
			return true
		}
	}
	return false
}

func isAwkIdentStart(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
}

var awkNames = []string{"awk", "gawk", "mawk", "nawk"}
