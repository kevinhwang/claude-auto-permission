// Package builtins ships the per-command Checker implementations (awk, sed, and aliases). Wire them in via
// DefaultRegistry.
package builtins

import "claude-auto-permission/internal/staticbash/cmdcheck"

// DefaultRegistry returns a fresh *cmdcheck.Registry populated with the builtin checkers (awk, sed and their aliases).
func DefaultRegistry() *cmdcheck.Registry {
	r := cmdcheck.NewRegistry()
	r.Register(awkChecker{}, awkNames...)
	r.Register(sedChecker{}, sedNames...)
	return r
}
