package staticbash

import (
	"context"
	"encoding/json"
	"testing"

	"claude-auto-permission/internal/config"
	"claude-auto-permission/internal/decider"
	configpb "claude-auto-permission/internal/gen/config/v1"
	"claude-auto-permission/internal/hookio"
)

// resolverWithDefaults returns a config.Resolver matching cwd "/**" with the default rule set enabled — the same shape
// the README's minimal config produces.
func resolverWithDefaults(t *testing.T) *config.Resolver {
	t.Helper()
	cfg := &configpb.Config{}
	proj := &configpb.Project{}
	proj.SetPathPatterns([]string{"/**"})
	sbr := &configpb.StaticBashRules{}
	sbr.SetUseDefaultRules(&configpb.UseDefaultRules{})
	proj.SetStaticBashRules(sbr)
	cfg.SetProjects([]*configpb.Project{proj})
	return config.NewResolver(cfg)
}

func bashInput(cmd string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"command": cmd})
	return b
}

func TestDecide_NotBash(t *testing.T) {
	t.Parallel()
	d := New(resolverWithDefaults(t))
	got := d.Decide(context.Background(), &hookio.HookInput{
		ToolName:  "Read",
		ToolInput: json.RawMessage(`{"file_path":"x"}`),
	}, decider.Env{})
	if got.Decision != decider.DecisionSilent {
		t.Errorf("Decision = %q, want silent", got.Decision)
	}
	if got.Reason != "not bash" {
		t.Errorf("Reason = %q, want %q", got.Reason, "not bash")
	}
}

func TestDecide_NilInput(t *testing.T) {
	t.Parallel()
	d := New(resolverWithDefaults(t))
	got := d.Decide(context.Background(), nil, decider.Env{})
	if got.Decision != decider.DecisionSilent {
		t.Errorf("Decision = %q, want silent", got.Decision)
	}
}

func TestDecide_MalformedToolInput(t *testing.T) {
	t.Parallel()
	d := New(resolverWithDefaults(t))
	got := d.Decide(context.Background(), &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: json.RawMessage(`{this is not json`),
	}, decider.Env{})
	if got.Decision != decider.DecisionSilent {
		t.Errorf("Decision = %q, want silent", got.Decision)
	}
	if got.Reason != "malformed tool input json" {
		t.Errorf("Reason = %q, want %q", got.Reason, "malformed tool input json")
	}
}

func TestDecide_EmptyCommand(t *testing.T) {
	t.Parallel()
	d := New(resolverWithDefaults(t))
	got := d.Decide(context.Background(), &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: bashInput(""),
	}, decider.Env{})
	if got.Decision != decider.DecisionSilent {
		t.Errorf("Decision = %q, want silent", got.Decision)
	}
	if got.Reason != "empty command" {
		t.Errorf("Reason = %q, want %q", got.Reason, "empty command")
	}
}

func TestDecide_AllowedCommand(t *testing.T) {
	t.Parallel()
	d := New(resolverWithDefaults(t))
	got := d.Decide(context.Background(), &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: bashInput("git status"),
		Cwd:       "/tmp",
	}, decider.Env{})
	if got.Decision != decider.DecisionAllow {
		t.Errorf("Decision = %q, want allow", got.Decision)
	}
	if got.Reason != "matched static rule: git" {
		t.Errorf("Reason = %q, want %q", got.Reason, "matched static rule: git")
	}
}

func TestDecide_AllowedCompound_NamesMultipleRules(t *testing.T) {
	t.Parallel()
	d := New(resolverWithDefaults(t))
	got := d.Decide(context.Background(), &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: bashInput("ls && git status"),
		Cwd:       "/tmp",
	}, decider.Env{})
	if got.Decision != decider.DecisionAllow {
		t.Errorf("Decision = %q, want allow", got.Decision)
	}
	if got.Reason != "matched static rules: ls, git" {
		t.Errorf("Reason = %q, want %q", got.Reason, "matched static rules: ls, git")
	}
}

func TestDecide_UnknownCommand(t *testing.T) {
	t.Parallel()
	d := New(resolverWithDefaults(t))
	got := d.Decide(context.Background(), &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: bashInput("very_definitely_not_a_real_command --frobnicate"),
		Cwd:       "/tmp",
	}, decider.Env{})
	if got.Decision != decider.DecisionSilent {
		t.Errorf("Decision = %q, want silent", got.Decision)
	}
	if got.Reason != "no static rule matched" {
		t.Errorf("Reason = %q, want %q", got.Reason, "no static rule matched")
	}
}

func TestDecide_DangerousFlagFallsThrough(t *testing.T) {
	t.Parallel()
	d := New(resolverWithDefaults(t))
	// `git -c ...` is intentionally not allowlisted — sets arbitrary config and is one of the canonical fall-through
	// cases.
	got := d.Decide(context.Background(), &hookio.HookInput{
		ToolName:  "Bash",
		ToolInput: bashInput("git -c user.email=x@x.com status"),
		Cwd:       "/tmp",
	}, decider.Env{})
	if got.Decision != decider.DecisionSilent {
		t.Errorf("Decision = %q, want silent", got.Decision)
	}
}

// TestDecide_Name verifies the decider's stable identifier so downstream log consumers don't get surprised by a rename.
func TestDecide_Name(t *testing.T) {
	t.Parallel()
	d := New(resolverWithDefaults(t))
	if got := d.Name(); got != decider.NameStaticBash {
		t.Errorf("Name = %q, want %q", got, decider.NameStaticBash)
	}
	if decider.NameStaticBash != "static_bash_rules" {
		t.Errorf("NameStaticBash = %q, want static_bash_rules", decider.NameStaticBash)
	}
}
