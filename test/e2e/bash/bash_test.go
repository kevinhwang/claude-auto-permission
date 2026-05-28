// Package bash exercises the static bash rule engine conformance.
package bash

import (
	"os"
	"path/filepath"
	"testing"

	"claude-auto-permission/test/e2e/internal/runner"
)

var bins runner.Binaries

func TestMain(m *testing.M) {
	if os.Getenv("CLAUDE_AUTO_PERMISSION_E2E") != "1" {
		os.Exit(0)
	}
	dir, err := os.MkdirTemp("", "cap-bash-bins-")
	if err != nil {
		panic(err)
	}
	bins = runner.BuildBinariesDir(dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func TestBash(t *testing.T) {
	wd, _ := os.Getwd()
	casesDir := filepath.Join(wd, "cases")
	paths := runner.DiscoverCases(t, casesDir)
	if len(paths) == 0 {
		t.Skip("no cases found")
	}
	for _, p := range paths {
		c := runner.ParseCase(t, p)
		t.Run(c.Name, func(t *testing.T) {
			res := runner.Run(t, bins, c, "")
			runner.AssertVerdict(t, res, c.Expected)
		})
	}
}
