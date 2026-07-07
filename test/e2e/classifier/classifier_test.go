// Package classifier holds the LLM classifier evals. Each case is a behavioral eval of the whole judgment pipeline —
// the model, the prompt scaffolding, and the bundled baseline policy — against a curated allow/deny scenario. Unlike
// the deterministic bash conformance suite, a verdict here is one sample of a model-in-the-loop system, so a failure
// can mean a model regression, a prompt bug, or a policy-wording change. Cases hit real Bedrock — run with valid AWS
// credentials.
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

	for _, p := range paths {
		c := runner.ParseCase(t, p)

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
				t.Parallel()
				res := runner.Run(t, bins, c, mode)
				runner.AssertVerdict(t, res, c.Expected)
			})
		}
	}
}
