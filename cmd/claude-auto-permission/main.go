// Command claude-auto-permission is the Claude Code PreToolUse hook entry point. Wiring lives in internal/app.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"buf.build/go/protovalidate"
	"github.com/spf13/cobra"

	"claude-auto-permission/internal/app"
	"claude-auto-permission/internal/config"
	"claude-auto-permission/internal/config/loader"
	configpb "claude-auto-permission/internal/gen/config/v1"
	"claude-auto-permission/internal/logging"
)

const defaultConfigRel = ".config/claude-auto-permission/config.txtpb"

func main() {
	cfg := &configpb.Config{}

	bindings, err := loader.Walk(cfg.ProtoReflect().Descriptor())
	if err != nil {
		logging.Default().Error("schema walk failed", "err", err)
		os.Exit(1)
	}

	root := &cobra.Command{
		Use:   "claude-auto-permission",
		Short: "Claude Code PreToolUse hook for auto-approving safe tool calls",
		Long: `A Claude Code PreToolUse hook that auto-approves safe Bash commands via
a static rule engine, and (when enabled) consults an LLM classifier for
tool calls the static engine cannot decide.

Reads a hook event as JSON from stdin and writes the decision (if any)
as JSON to stdout.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			env := loader.EnvSlice(os.Environ())

			if err := loader.ApplyDefaults(cfg, bindings); err != nil {
				return fmt.Errorf("apply schema defaults: %w", err)
			}

			path, _ := cmd.Flags().GetString(loader.ConfigPathFlag)
			if path == "" {
				path = env[loader.ConfigPathEnv]
			}
			if path == "" {
				path = defaultConfigPath()
			}
			if path != "" {
				data, err := os.ReadFile(path)
				switch {
				case err == nil:
					if err := loader.ApplyFile(cfg, data); err != nil {
						return fmt.Errorf("parse config %s: %w", path, err)
					}
				case os.IsNotExist(err):
					// Tolerate a missing config; env and flag layers still apply over schema defaults.
				default:
					return fmt.Errorf("read config %s: %w", path, err)
				}
			}

			if err := loader.ApplyEnv(cfg, bindings, env); err != nil {
				return err
			}
			if err := loader.ApplyFlags(cfg, bindings, cmd.Flags()); err != nil {
				return err
			}
			if err := protovalidate.Validate(cfg); err != nil {
				return fmt.Errorf("validate config: %w", err)
			}

			return app.New(config.NewResolver(cfg)).Run(os.Stdin, os.Stdout)
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	root.PersistentFlags().String(loader.ConfigPathFlag, "", "Path to the textproto config file.")
	loader.RegisterFlags(root.PersistentFlags(), bindings)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// defaultConfigPath returns $HOME/.config/claude-auto-permission/config.txtpb, or empty when HOME is unresolvable.
// Callers tolerate a missing path.
func defaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, defaultConfigRel)
}
