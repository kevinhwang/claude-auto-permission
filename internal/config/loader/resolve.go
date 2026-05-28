package loader

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/pflag"
	"google.golang.org/protobuf/proto"
)

// ConfigPathEnv is the env-var name that overrides the textproto config file path. Hand-wired (not derived from the
// schema) because the path must resolve before the schema-driven binding pipeline can read the file.
const ConfigPathEnv = "CLAUDE_AUTO_PERMISSION_CONFIG"

// ConfigPathFlag is the CLI flag name that overrides the textproto config file path. Hand-wired alongside
// ConfigPathEnv.
const ConfigPathFlag = "config"

// Options bundles caller-supplied inputs to Resolve. Tests that don't need the real environment leave Env nil; nil is
// treated as the empty environment.
type Options struct {
	// Args is the command line, excluding argv[0]. Typically os.Args[1:]. Pass nil to skip flag parsing entirely.
	Args []string

	// Env maps env-var names to values. Typically envSlice(os.Environ()). nil ⇒ empty map.
	Env map[string]string

	// DefaultConfigPath is the textproto path to read when neither the --config flag nor ConfigPathEnv specifies one.
	// Empty means no file is read.
	DefaultConfigPath string

	// ExtraFlags lets callers register additional flags on the same FlagSet before parsing — useful when a cobra
	// subcommand wants its own flags to share Resolve's argv. Names must not collide with any schema-derived flag.
	ExtraFlags func(fs *pflag.FlagSet)
}

// Resolve runs the full precedence pipeline against msg:
//  1. Walk the descriptor; build a binding registry.
//  2. Bootstrap-resolve the config-file path from --config / ConfigPathEnv / Options.DefaultConfigPath.
//  3. Apply schema defaults to msg.
//  4. If a config file was named and exists, prototext-unmarshal it onto msg.
//  5. Apply env-var overrides for every annotated field with an env_var binding present in opts.Env.
//  6. Apply CLI-flag overrides for every flag the user explicitly set.
//
// The returned error covers schema-walk failures (annotated non-scalar field), default parse errors, env/flag parse
// errors, and file read / parse errors. Validation against (buf.validate) constraints is the caller's responsibility —
// the loader is a generic-over-message-type pipeline and shouldn't bake in a particular validation policy.
func Resolve(msg proto.Message, opts Options) error {
	bindings, err := Walk(msg.ProtoReflect().Descriptor())
	if err != nil {
		return err
	}

	fs := pflag.NewFlagSet("claude-auto-permission", pflag.ContinueOnError)
	fs.SortFlags = false
	// Silence the FlagSet's own usage / error printing — Resolve's caller (cobra) will format errors how it likes.
	fs.SetOutput(discardWriter{})

	var configPath string
	fs.StringVar(&configPath, ConfigPathFlag, "", "Path to the textproto config file.")

	RegisterFlags(fs, bindings)
	if opts.ExtraFlags != nil {
		opts.ExtraFlags(fs)
	}

	if opts.Args != nil {
		if err := fs.Parse(opts.Args); err != nil {
			return fmt.Errorf("parse flags: %w", err)
		}
	}

	// Bootstrap the config-file path: flag wins, then env, then the caller's default. Tilde expansion is the caller's job;
	// the loader keeps no opinion about path semantics.
	path := configPath
	if path == "" {
		if v := opts.Env[ConfigPathEnv]; v != "" {
			path = v
		}
	}
	if path == "" {
		path = opts.DefaultConfigPath
	}

	if err := ApplyDefaults(msg, bindings); err != nil {
		return err
	}

	if path != "" {
		data, err := os.ReadFile(path)
		switch {
		case err == nil:
			if err := ApplyFile(msg, data); err != nil {
				return fmt.Errorf("parse config %s: %w", path, err)
			}
		case os.IsNotExist(err):
			// Missing file is tolerated: env and flag layers still run on the schema defaults, which yields the empty
			// (most-restrictive) config. Callers log the path.
		default:
			return fmt.Errorf("read config %s: %w", path, err)
		}
	}

	if err := ApplyEnv(msg, bindings, opts.Env); err != nil {
		return err
	}
	if err := ApplyFlags(msg, bindings, fs); err != nil {
		return err
	}
	return nil
}

// EnvSlice converts an [] string of "KEY=VALUE" entries (the shape os.Environ returns) into a map[string]string
// suitable for Options.Env.
func EnvSlice(env []string) map[string]string {
	out := make(map[string]string, len(env))
	for _, kv := range env {
		i := strings.IndexByte(kv, '=')
		if i < 0 {
			continue
		}
		out[kv[:i]] = kv[i+1:]
	}
	return out
}

// discardWriter is a tiny io.Writer that swallows everything. Used to silence pflag's own error printing.
type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
