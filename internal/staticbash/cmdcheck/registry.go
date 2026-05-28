package cmdcheck

// Registry maps command names to Checkers. Used by the walker to dispatch commands without a rule-engine spec (sed,
// awk, …).
//
// Construct with NewRegistry — the zero value would nil-map-panic on Register. Explicit values (rather than a
// process-global) let tests pick exactly the checkers they care about.
type Registry struct {
	checkers map[string]Checker
}

func NewRegistry() *Registry {
	return &Registry{checkers: map[string]Checker{}}
}

// Register maps each name to c. Later Registers overwrite — overlays can override builtins.
func (r *Registry) Register(c Checker, names ...string) {
	for _, name := range names {
		r.checkers[name] = c
	}
}

func (r *Registry) Lookup(name string) (Checker, bool) {
	c, ok := r.checkers[name]
	return c, ok
}
