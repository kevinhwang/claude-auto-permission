// Package classifier exercises the LLM classifier conformance. Cases hit real Bedrock — run with valid AWS credentials.
package classifier

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
	dir, err := os.MkdirTemp("", "cap-classifier-bins-")
	if err != nil {
		panic(err)
	}
	bins = runner.BuildBinariesDir(dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func TestClassifier(t *testing.T) {
	wd, _ := os.Getwd()
	casesDir := filepath.Join(wd, "cases")
	paths := runner.DiscoverCases(t, casesDir)
	if len(paths) == 0 {
		t.Skip("no cases found")
	}

	quickOnly := os.Getenv("CLAUDE_AUTO_PERMISSION_E2E_FULL") != "1"

	for _, p := range paths {
		c := runner.ParseCase(t, p)
		if quickOnly && !c.HasTag("quick") {
			continue
		}

		modes := c.Modes
		if len(modes) == 0 {
			modes = []string{""}
		}

		for _, mode := range modes {
			name := c.Name
			if mode != "" {
				name += "/" + mode
			}
			t.Run(name, func(t *testing.T) {
				res := runner.Run(t, bins, c, mode)
				runner.AssertVerdict(t, res, c.Expected)
			})
		}
	}
}
