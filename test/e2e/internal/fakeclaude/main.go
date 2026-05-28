// Command fakeclaude is a minimal shim used by e2e tests to replace the real `claude` binary. It implements only
// `claude auto-mode config`, reading the policy JSON from a path specified by the CLAUDE_FAKE_AUTO_MODE_POLICY_PATH env
// var.
//
// Build it once at TestMain and prepend its directory to PATH so the hook's automodepolicy.Loader shells out to this
// instead of a real `claude` install.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

const policyPathEnv = "CLAUDE_FAKE_AUTO_MODE_POLICY_PATH"

func main() {
	root := &cobra.Command{
		Use:           "claude",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	autoMode := &cobra.Command{
		Use: "auto-mode",
	}

	configCmd := &cobra.Command{
		Use: "config",
		RunE: func(_ *cobra.Command, _ []string) error {
			path := os.Getenv(policyPathEnv)
			if path == "" {
				return fmt.Errorf("fakeclaude: %s not set", policyPathEnv)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("fakeclaude: read %s: %w", path, err)
			}
			_, _ = os.Stdout.Write(data)
			return nil
		},
	}

	autoMode.AddCommand(configCmd)
	root.AddCommand(autoMode)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
