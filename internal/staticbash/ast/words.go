package ast

import "mvdan.cc/sh/v3/syntax"

// wordIsSafe checks whether a word (in argument position) contains only safe expansions. Command substitutions are
// allowed only if the inner commands are themselves safe.
func (w *walker) wordIsSafe(word *syntax.Word) bool {
	if word == nil {
		return true
	}
	return w.partsAreSafe(word.Parts)
}

func (w *walker) partsAreSafe(parts []syntax.WordPart) bool {
	for _, part := range parts {
		if !w.partIsSafe(part) {
			return false
		}
	}
	return true
}

func (w *walker) partIsSafe(part syntax.WordPart) bool {
	switch p := part.(type) {
	case *syntax.Lit:
		return true
	case *syntax.SglQuoted:
		return true
	case *syntax.DblQuoted:
		return w.partsAreSafe(p.Parts)
	case *syntax.ParamExp:
		return w.paramExpIsSafe(p)
	case *syntax.CmdSubst:
		return w.stmtsAreSafe(p.Stmts)
	case *syntax.ProcSubst:
		return w.stmtsAreSafe(p.Stmts)
	case *syntax.ArithmExp:
		return w.arithmExpIsSafe(p)
	case *syntax.ExtGlob:
		return true
	case *syntax.BraceExp:
		return true
	default:
		return false
	}
}

func (w *walker) paramExpIsSafe(pe *syntax.ParamExp) bool {
	if pe.Exp != nil && pe.Exp.Word != nil {
		if !w.wordIsSafe(pe.Exp.Word) {
			return false
		}
	}
	if pe.Repl != nil {
		if pe.Repl.Orig != nil && !w.wordIsSafe(pe.Repl.Orig) {
			return false
		}
		if pe.Repl.With != nil && !w.wordIsSafe(pe.Repl.With) {
			return false
		}
	}
	if pe.Slice != nil {
		if !w.arithmExprIsSafe(pe.Slice.Offset) {
			return false
		}
		if pe.Slice.Length != nil && !w.arithmExprIsSafe(pe.Slice.Length) {
			return false
		}
	}
	return true
}

func (w *walker) arithmExpIsSafe(ae *syntax.ArithmExp) bool {
	if ae == nil {
		return true
	}
	return w.arithmExprIsSafe(ae.X)
}

func (w *walker) arithmExprIsSafe(expr syntax.ArithmExpr) bool {
	if expr == nil {
		return true
	}
	switch e := expr.(type) {
	case *syntax.BinaryArithm:
		return w.arithmExprIsSafe(e.X) && w.arithmExprIsSafe(e.Y)
	case *syntax.UnaryArithm:
		return w.arithmExprIsSafe(e.X)
	case *syntax.ParenArithm:
		return w.arithmExprIsSafe(e.X)
	case *syntax.Word:
		return w.wordIsSafe(e)
	default:
		return false
	}
}

func (w *walker) testExprIsSafe(expr syntax.TestExpr) bool {
	if expr == nil {
		return true
	}
	switch e := expr.(type) {
	case *syntax.BinaryTest:
		return w.testExprIsSafe(e.X) && w.testExprIsSafe(e.Y)
	case *syntax.UnaryTest:
		return w.testExprIsSafe(e.X)
	case *syntax.ParenTest:
		return w.testExprIsSafe(e.X)
	case *syntax.Word:
		return w.wordIsSafe(e)
	default:
		return false
	}
}
