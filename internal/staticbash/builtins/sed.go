package builtins

import (
	"strings"

	"claude-auto-permission/internal/staticbash/cmdcheck"

	"mvdan.cc/sh/v3/syntax"
)

type sedChecker struct{}

func (sedChecker) Check(_ *cmdcheck.Context, args []*syntax.Word) bool {
	if cmdcheck.HasWriteFlags(args, "i", "--in-place") {
		return false
	}
	return checkSedProgram(args)
}

func checkSedProgram(args []*syntax.Word) bool {
	i := 1
	for i < len(args) {
		s, ok := cmdcheck.LiteralString(args[i])
		if !ok {
			return false
		}
		if s == "-e" || s == "--expression" {
			if i+1 >= len(args) {
				return false
			}
			prog, ok := cmdcheck.LiteralString(args[i+1])
			if !ok {
				return false
			}
			if !sedProgramIsSafe(prog) {
				return false
			}
			i += 2
			continue
		}
		if s == "-f" || s == "--file" {
			return false
		}
		if strings.HasPrefix(s, "-") {
			i++
			continue
		}
		// First non-flag, non-option argument is the program.
		return sedProgramIsSafe(s)
	}
	return true
}

// Allowed flags after s/pat/repl/FLAGS.
const sedSafeSubstFlags = "gpiI0123456789"

// sedProgramIsSafe rejects any sed command not on the allowlist — notably w, W, e (write/exec) plus anything
// unrecognized.
func sedProgramIsSafe(prog string) bool {
	n := len(prog)
	i := 0
	for i < n {
		// Skip command separators.
		for i < n && (prog[i] == ' ' || prog[i] == '\t' || prog[i] == ';' || prog[i] == '\n' || prog[i] == '}') {
			i++
		}
		if i >= n {
			break
		}

		i = skipSedAddress(prog, i)
		if i >= n {
			break
		}
		// Optional second address for ranges (addr1,addr2 cmd).
		if i < n && prog[i] == ',' {
			i++
			i = skipSedAddress(prog, i)
		}
		// Skip whitespace and the optional negation operator (!).
		for i < n && (prog[i] == ' ' || prog[i] == '\t' || prog[i] == '!') {
			i++
		}
		if i >= n {
			break
		}

		ch := prog[i]

		// `{` starts a block; commands inside are checked on the next iteration.
		if ch == '{' {
			i++
			continue
		}

		// s/y: parse delimited form, then validate flags.
		if ch == 's' {
			i++
			if i >= n {
				return false
			}
			i = skipSedDelimited(prog, i, 2)
			if i < 0 {
				return false
			}
			// Check flags after the closing delimiter.
			for i < n && prog[i] != ';' && prog[i] != '\n' && prog[i] != '}' {
				if !strings.ContainsRune(sedSafeSubstFlags, rune(prog[i])) {
					return false
				}
				i++
			}
			continue
		}
		if ch == 'y' {
			i++
			if i >= n {
				return false
			}
			i = skipSedDelimited(prog, i, 2)
			if i < 0 {
				return false
			}
			continue
		}

		// b/t/T: branch/test — skip optional label.
		if ch == 'b' || ch == 't' || ch == 'T' {
			i++
			for i < n && prog[i] == ' ' {
				i++
			}
			for i < n && prog[i] != ';' && prog[i] != '\n' && prog[i] != '}' {
				i++
			}
			continue
		}

		// a/i/c: append/insert/change — skip the text argument.
		if ch == 'a' || ch == 'i' || ch == 'c' {
			i++
			if i < n && prog[i] == '\\' {
				i++
			}
			for i < n && prog[i] != '\n' {
				i++
			}
			continue
		}

		// r/R: read file into output. Safe — reads, doesn't write.
		if ch == 'r' || ch == 'R' {
			i++
			for i < n && prog[i] != ';' && prog[i] != '\n' {
				i++
			}
			continue
		}

		// Safe single-char commands with no args.
		if strings.ContainsRune("dDpPnNqQlxhHgGz=", rune(ch)) {
			i++
			continue
		}

		// :label
		if ch == ':' {
			i++
			for i < n && prog[i] != ';' && prog[i] != '\n' && prog[i] != '}' {
				i++
			}
			continue
		}

		// Unrecognized (including w, W, e) — deny.
		return false
	}
	return true
}

func skipSedAddress(prog string, i int) int {
	n := len(prog)
	if i >= n {
		return i
	}
	// /regex/ address
	if prog[i] == '/' || prog[i] == '\\' {
		delim := prog[i]
		if delim == '\\' {
			i++
			if i >= n {
				return i
			}
			delim = prog[i]
		}
		i++
		for i < n {
			if prog[i] == '\\' {
				i += 2
				continue
			}
			if prog[i] == delim {
				i++
				break
			}
			i++
		}
		return i
	}
	// $ address
	if prog[i] == '$' {
		return i + 1
	}
	// Numeric address
	if prog[i] >= '0' && prog[i] <= '9' {
		for i < n && prog[i] >= '0' && prog[i] <= '9' {
			i++
		}
		// Optional step: ~N
		if i < n && prog[i] == '~' {
			i++
			for i < n && prog[i] >= '0' && prog[i] <= '9' {
				i++
			}
		}
		return i
	}
	return i
}

// skipSedDelimited skips count delimited sections (e.g., 2 for s/pat/repl/). Returns position after the closing
// delimiter, or -1 on error.
func skipSedDelimited(prog string, i int, count int) int {
	n := len(prog)
	if i >= n {
		return -1
	}
	delim := prog[i]
	i++
	seen := 0
	for i < n && seen < count {
		if prog[i] == '\\' {
			i += 2
			continue
		}
		if prog[i] == delim {
			seen++
		}
		i++
	}
	if seen < count {
		return -1
	}
	return i
}

var sedNames = []string{"sed", "gsed"}
