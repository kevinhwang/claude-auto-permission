// Package runner is the e2e test harness. It parses txtar test cases, lays out a hermetic filesystem in a temp dir,
// runs the hook binary as a subprocess, and asserts on the verdict.
package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/tools/txtar"
	"gopkg.in/yaml.v3"

	"claude-auto-permission/internal/hookio"
)

// Case is the parsed representation of one txtar test case.
type Case struct {
	Name        string
	Description string
	Tool        string
	Expected    Expected
	Modes       []string // classifier_modes
	Tags        []string

	// File contents extracted from the txtar archive.
	TranscriptJSONL string
	ToolInputJSON   string
	EffectivePolicy string
	ConfigTxtpb     string
	RepoFiles       map[string]string // relative path → content
	HomeFiles       map[string]string // relative path → content
}

// Expected verdict assertions.
type Expected struct {
	Combined string            `yaml:"combined"`
	Deciders map[string]string `yaml:"deciders,omitempty"`
}

// caseYAML is the intermediate parse shape for case.yaml.
type caseYAML struct {
	Description     string   `yaml:"description"`
	Tool            string   `yaml:"tool"`
	Expected        Expected `yaml:"expected"`
	ClassifierModes []string `yaml:"classifier_modes"`
	Tags            []string `yaml:"tags"`
}

// ParseCase reads a txtar file and returns the parsed Case.
func ParseCase(t *testing.T, path string) Case {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read case %s: %v", path, err)
	}
	ar := txtar.Parse(data)

	c := Case{
		Name:      strings.TrimSuffix(filepath.Base(path), ".txtar"),
		RepoFiles: map[string]string{},
		HomeFiles: map[string]string{},
	}

	for _, f := range ar.Files {
		content := string(f.Data)
		switch {
		case f.Name == "case.yaml":
			var cy caseYAML
			if err := yaml.Unmarshal(f.Data, &cy); err != nil {
				t.Fatalf("parse case.yaml in %s: %v", path, err)
			}
			c.Description = cy.Description
			c.Tool = cy.Tool
			c.Expected = cy.Expected
			c.Modes = cy.ClassifierModes
			c.Tags = cy.Tags
		case f.Name == "transcript.jsonl":
			c.TranscriptJSONL = content
		case f.Name == "tool_input.json":
			c.ToolInputJSON = content
		case f.Name == "claude/auto-mode-effective-policy.json":
			c.EffectivePolicy = content
		case f.Name == "config.txtpb":
			c.ConfigTxtpb = content
		case strings.HasPrefix(f.Name, "repo/"):
			c.RepoFiles[strings.TrimPrefix(f.Name, "repo/")] = content
		case strings.HasPrefix(f.Name, "home/"):
			c.HomeFiles[strings.TrimPrefix(f.Name, "home/")] = content
		}
	}

	if c.Tool == "" {
		t.Fatalf("case %s: case.yaml missing 'tool' field", path)
	}
	if c.Expected.Combined == "" {
		t.Fatalf("case %s: case.yaml missing 'expected.combined'", path)
	}
	return c
}

// Binaries holds paths to pre-built binaries used by the harness.
type Binaries struct {
	Hook       string // claude-auto-permission binary
	FakeClaude string // fakeclaude shim
}

// Result from running one case.
type Result struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
	CacheDir string
}

// Run executes a single case against the hook binary and returns the raw result. Does not assert — caller does that.
func Run(t *testing.T, bins Binaries, c Case, modeOverride string) Result {
	t.Helper()

	dir := t.TempDir()
	homeDir := filepath.Join(dir, "home")
	claudeConfigDir := filepath.Join(homeDir, ".claude")
	repoDir := filepath.Join(dir, "repo")
	cacheDir := filepath.Join(dir, "cache")
	binDir := filepath.Join(dir, "bin")

	for _, d := range []string{claudeConfigDir, repoDir, cacheDir, binDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	// Symlink fakeclaude into bin/ as "claude".
	if err := os.Symlink(bins.FakeClaude, filepath.Join(binDir, "claude")); err != nil {
		t.Fatalf("symlink fakeclaude: %v", err)
	}

	// Write repo files.
	for rel, content := range c.RepoFiles {
		p := filepath.Join(repoDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("write repo file: %v", err)
		}
	}

	// Write home files.
	for rel, content := range c.HomeFiles {
		p := filepath.Join(homeDir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("write home file: %v", err)
		}
	}

	// Write transcript.
	transcriptPath := ""
	if c.TranscriptJSONL != "" {
		transcriptPath = filepath.Join(dir, "transcript.jsonl")
		if err := os.WriteFile(transcriptPath, []byte(c.TranscriptJSONL), 0o600); err != nil {
			t.Fatalf("write transcript: %v", err)
		}
	}

	// Write effective policy for fakeclaude.
	policyPath := filepath.Join(dir, "effective-policy.json")
	policy := c.EffectivePolicy
	if policy == "" {
		policy = `{"allow":[],"soft_deny":[],"hard_deny":[],"environment":[]}`
	}
	if err := os.WriteFile(policyPath, []byte(policy), 0o600); err != nil {
		t.Fatalf("write policy: %v", err)
	}

	// Write config.
	configPath := filepath.Join(dir, "config.txtpb")
	configContent := c.ConfigTxtpb
	if configContent == "" {
		configContent = defaultConfig(modeOverride)
	} else if modeOverride != "" {
		configContent = injectTwoStage(configContent, modeOverride)
	}
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	input := hookio.HookInput{
		HookEventName:  hookio.EventPreToolUse,
		ToolName:       c.Tool,
		ToolInput:      json.RawMessage(c.ToolInputJSON),
		Cwd:            repoDir,
		SessionId:      "e2e-" + c.Name,
		TranscriptPath: transcriptPath,
	}
	stdinBytes, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal hook input: %v", err)
	}

	// Build env. CLAUDE_CONFIG_DIR redirects all user-tier reads (settings.json, CLAUDE.md) away from the real ~/.claude.
	// HOME is NOT overridden — the AWS SDK and pathutil.ExpandTilde need the real home. Our hermeticity guarantee comes
	// from CLAUDE_CONFIG_DIR + CLAUDE_AUTO_PERMISSION_* overrides, not from HOME redirection.
	env := []string{
		"CLAUDE_CONFIG_DIR=" + claudeConfigDir,
		"CLAUDE_PROJECT_DIR=" + repoDir,
		"CLAUDE_AUTO_PERMISSION_CONFIG=" + configPath,
		"CLAUDE_AUTO_PERMISSION_RUNTIME_CACHE_DIR=" + cacheDir,
		"CLAUDE_FAKE_AUTO_MODE_POLICY_PATH=" + policyPath,
		"PATH=" + binDir + ":/usr/bin:/bin",
	}
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "HOME=") {
			env = append(env, e)
			continue
		}
		// Pass through AWS_* but skip empty values — empty AWS_ACCESS_KEY_ID poisons the SDK credential chain.
		if strings.HasPrefix(e, "AWS_") {
			if _, v, _ := strings.Cut(e, "="); v != "" {
				env = append(env, e)
			}
		}
	}

	cmd := exec.Command(bins.Hook)
	cmd.Stdin = bytes.NewReader(stdinBytes)
	cmd.Dir = repoDir
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := runWithTimeout(cmd, 60*time.Second)

	exitCode := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("run hook: %v\nstderr=%s", runErr, stderr.String())
		}
	}

	return Result{
		ExitCode: exitCode,
		Stdout:   stdout.Bytes(),
		Stderr:   stderr.Bytes(),
		CacheDir: cacheDir,
	}
}

// AssertVerdict checks the hook output against the expected verdict.
func AssertVerdict(t *testing.T, res Result, expected Expected) {
	t.Helper()

	if res.ExitCode != 0 {
		t.Fatalf("exit %d, stderr=%s", res.ExitCode, res.Stderr)
	}

	got := parseVerdict(t, res.Stdout)
	if got != expected.Combined {
		t.Errorf("verdict = %q, want %q\nstdout=%s\nstderr=%s",
			got, expected.Combined, res.Stdout, res.Stderr)
	}

	if len(expected.Deciders) > 0 {
		assertDeciders(t, res.CacheDir, expected.Deciders)
	}
}

func parseVerdict(t *testing.T, stdout []byte) string {
	t.Helper()
	trimmed := bytes.TrimSpace(stdout)
	if len(trimmed) == 0 {
		return "silent"
	}
	var out hookio.PreToolUseOutput
	if err := json.Unmarshal(trimmed, &out); err != nil {
		t.Fatalf("parse output: %v\nraw=%s", err, stdout)
	}
	dec := out.HookSpecificOutput.PermissionDecision
	if dec == "" {
		return "silent"
	}
	return dec
}

func assertDeciders(t *testing.T, cacheDir string, expected map[string]string) {
	t.Helper()
	logPath := filepath.Join(cacheDir, "decisions.log.jsonl")
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read decision log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) == 0 {
		t.Fatal("decision log empty")
	}
	// Take the last line (the decision for this tool call).
	var entry struct {
		Deciders map[string]struct {
			Decision string `json:"decision"`
		} `json:"deciders"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &entry); err != nil {
		t.Fatalf("parse decision log entry: %v", err)
	}
	for name, want := range expected {
		got, ok := entry.Deciders[name]
		if !ok {
			t.Errorf("decider %q not in log entry", name)
			continue
		}
		if got.Decision != want {
			t.Errorf("decider %q: got %q, want %q", name, got.Decision, want)
		}
	}
}

func defaultConfig(twoStage string) string {
	if twoStage == "" {
		twoStage = "TWO_STAGE_BOTH"
	}
	return `projects {
  path_patterns: "/**"
  static_bash_rules {
    use_default_rules {}
  }
  llm_classifier {
    enabled: true
    bedrock {
      model_id: "us.anthropic.claude-haiku-4-5-20251001-v1:0"
      two_stage: ` + twoStage + `
    }
    timeout_ms: 30000
    log_decisions: true
  }
}
`
}

func injectTwoStage(config, mode string) string {
	if strings.Contains(config, "two_stage:") {
		return config
	}
	return strings.Replace(config, "bedrock {", "bedrock {\n      two_stage: "+mode, 1)
}

func runWithTimeout(cmd *exec.Cmd, d time.Duration) error {
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	done := make(chan error, 1)
	var once sync.Once
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(d):
		once.Do(func() { _ = cmd.Process.Kill() })
		<-done
		return fmt.Errorf("timed out after %s", d)
	}
}

// DiscoverCases finds all .txtar files under dir recursively.
func DiscoverCases(t *testing.T, dir string) []string {
	t.Helper()
	var paths []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(path, ".txtar") {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return paths
}

// HasTag reports whether the case has the given tag.
func (c Case) HasTag(tag string) bool {
	for _, t := range c.Tags {
		if t == tag {
			return true
		}
	}
	return false
}
