package rules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"claude-auto-permission/internal/config"
	configpb "claude-auto-permission/internal/gen/config/v1"
	"claude-auto-permission/internal/pathutil"
	"claude-auto-permission/internal/staticbash/builtins"
	"claude-auto-permission/internal/staticbash/policy"

	"mvdan.cc/sh/v3/syntax"
)

// testCheckers is the same builtin set production wires in. Tests that exercise sed/awk rules need it; tests that don't
// can leave EvalCtx.Checkers nil.
var (
	testCheckers = builtins.DefaultRegistry()
	testCfg      *config.Resolver
)

func TestMain(m *testing.M) {
	allowProjectWrite := true
	testCfg = config.NewResolver(configpb.Config_builder{
		Projects: []*configpb.Project{
			configpb.Project_builder{
				PathPatterns: []string{"/**"},
				StaticBashRules: configpb.StaticBashRules_builder{
					AllowWritePatterns: []string{"/tmp/**"},
				}.Build(),
			}.Build(),
			configpb.Project_builder{
				PathPatterns: []string{"/project/**"},
				StaticBashRules: configpb.StaticBashRules_builder{
					AllowProjectWrite: &allowProjectWrite,
					RemoteHosts: []*configpb.RemoteHost{configpb.RemoteHost_builder{
						HostPatterns:       []string{"allowed.example.com"},
						AllowWritePatterns: []string{"/tmp/**"},
					}.Build()},
				}.Build(),
			}.Build(),
		},
	}.Build())
	os.Exit(m.Run())
}

func parseTestWords(t *testing.T, cmd string) []*syntax.Word {
	t.Helper()
	f, err := syntax.NewParser().Parse(strings.NewReader(cmd), "")
	if err != nil {
		t.Fatalf("parse %q: %v", cmd, err)
	}
	if len(f.Stmts) != 1 {
		t.Fatalf("expected 1 stmt for %q, got %d", cmd, len(f.Stmts))
	}
	call, ok := f.Stmts[0].Cmd.(*syntax.CallExpr)
	if !ok {
		t.Fatalf("expected CallExpr for %q", cmd)
	}
	return call.Args
}

func testEvalCtx() *EvalCtx {
	rs, _ := DefaultRules()
	cwd := "/project"
	return &EvalCtx{
		Cwd:       cwd,
		WriteDirs: []string{"/tmp"},
		IsPathAllowed: func(path string) bool {
			if path == "/dev/null" {
				return true
			}
			resolved := path
			if !filepath.IsAbs(path) {
				resolved = filepath.Join(cwd, path)
			}
			for _, dir := range []string{cwd, "/tmp"} {
				if pathutil.IsPathUnder(resolved, dir) {
					return true
				}
			}
			return false
		},
		Evaluate: func(command, evalCwd string, writeDirs []string) bool {
			// Lightweight recursive evaluator for testing. Avoids importing walker (which would create an import cycle).
			cmd := strings.TrimSpace(command)
			parts := strings.Fields(cmd)
			if len(parts) == 0 {
				return false
			}
			safeCmds := map[string]bool{
				"git": true, "ls": true, "cat": true, "grep": true,
				"echo": true, "pwd": true, "head": true, "tail": true,
			}
			return safeCmds[parts[0]]
		},
		RemoteHostLookup: testRemoteHostLookup,
		RuleSet:          rs,
		Checkers:         testCheckers,
	}
}

// testRemoteHostLookup adapts policy.MatchRemoteHost to the rules engine's seam. Defined as a stand-alone helper so
// this test file stays the only place that imports the policy and config packages for the lookup wiring.
func testRemoteHostLookup(host, cwd string) (RemoteHostConfig, bool) {
	rh, ok := policy.MatchRemoteHost(testCfg, host, cwd)
	if !ok {
		return nil, false
	}
	return rh, true
}

func evalCmd(t *testing.T, cmdName, cmdStr string) bool {
	t.Helper()
	rs, err := DefaultRules()
	if err != nil {
		t.Fatalf("DefaultRules: %v", err)
	}
	spec := LookupCommand(rs, cmdName)
	if spec == nil {
		t.Fatalf("no spec for %q", cmdName)
	}
	args := parseTestWords(t, cmdStr)
	ctx := testEvalCtx()
	return Evaluate(spec, ctx, args)
}

// --------------------------------------------------------------------------- Always-safe commands
// ---------------------------------------------------------------------------

func TestAlwaysSafeCommands(t *testing.T) {
	alwaysSafe := []struct {
		name string
		cmd  string
	}{
		// Filesystem inspection
		{"cat file", "cat file.txt"},
		{"cat multiple", "cat a.txt b.txt"},
		{"head -n", "head -n 10 file.txt"},
		{"tail -f", "tail -f /var/log/syslog"},
		{"less file", "less README.md"},
		{"more file", "more README.md"},
		{"bat file", "bat --style=plain file.go"},
		{"ls bare", "ls"},
		{"ls -la", "ls -la /some/dir"},
		{"tree depth", "tree -L 3"},
		{"exa long", "exa -l"},
		{"eza long", "eza --long"},
		{"file check", "file image.png"},
		{"stat file", "stat file.txt"},
		{"du -sh", "du -sh /project"},
		{"df -h", "df -h"},
		{"pwd", "pwd"},

		// Text processing
		{"echo", "echo hello world"},
		{"printf fmt", "printf '%s\\n' hello"},
		{"grep pattern", "grep -r pattern ."},
		{"egrep", "egrep 'foo|bar' file.txt"},
		{"fgrep", "fgrep literal file.txt"},
		{"rg pattern", "rg --type go pattern"},
		{"ag pattern", "ag pattern src/"},
		{"ack pattern", "ack --go pattern"},
		{"wc -l", "wc -l file.txt"},
		{"uniq", "uniq file.txt"},
		{"cut", "cut -d: -f1 /etc/passwd"},
		{"tr", "tr '[:lower:]' '[:upper:]'"},
		{"rev", "rev file.txt"},
		{"tac", "tac file.txt"},
		{"diff", "diff a.txt b.txt"},
		{"cmp", "cmp file1 file2"},
		{"comm", "comm sorted1.txt sorted2.txt"},
		{"colordiff", "colordiff a.txt b.txt"},
		{"delta", "delta a.txt b.txt"},
		{"column", "column -t file.txt"},
		{"expand", "expand file.txt"},
		{"fold", "fold -w 80 file.txt"},
		{"fmt", "fmt -w 72 file.txt"},
		{"nl", "nl file.txt"},
		{"paste", "paste a.txt b.txt"},
		{"join", "join a.txt b.txt"},

		// Search / locate
		{"locate", "locate pattern"},
		{"mdfind", "mdfind something"},

		// macOS utilities
		{"screencapture bare", "screencapture out.png"},
		{"screencapture interactive", "screencapture -i /tmp/shot.png"},
		{"screencapture clipboard", "screencapture -c"},

		// Path utilities
		{"basename", "basename /path/to/file.txt"},
		{"dirname", "dirname /path/to/file.txt"},
		{"realpath", "realpath ./relative"},
		{"readlink", "readlink -f link"},

		// Checksums
		{"md5sum", "md5sum file.txt"},
		{"sha256sum", "sha256sum file.txt"},
		{"sha1sum", "sha1sum file.txt"},
		{"shasum", "shasum -a 256 file.txt"},
		{"md5", "md5 file.txt"},

		// Env / identity
		{"printenv", "printenv HOME"},
		{"uname -a", "uname -a"},
		{"hostname", "hostname"},
		{"id", "id"},
		{"whoami", "whoami"},
		{"groups", "groups"},
		{"date", "date +%Y-%m-%d"},
		{"cal", "cal 2026"},

		// Structured data
		{"jq", "jq '.foo' data.json"},

		// Shell builtins (export FOO=bar is a DeclClause, tested at walker level)
		{"cd", "cd /project/src"},
		{"pushd", "pushd /tmp"},
		{"popd", "popd"},
		{"true", "true"},
		{"false", "false"},
		{"test", "test -f file.txt"},
		{"which", "which git"},
		{"whereis", "whereis go"},
		{"type", "type ls"},
		{"hash", "hash -r"},
		{"seq", "seq 1 10"},
		{"mktemp", "mktemp -d"},
		{"sleep", "sleep 1"},
		{"set", "set -e"},

		// System info
		{"top", "top -l 1"},
		{"ps", "ps aux"},
		{"uptime", "uptime"},
		{"free", "free -m"},

		// Build tools
		{"bazel", "bazel build //..."},
		{"rustc", "rustc --version"},
		{"xcodebuild", "xcodebuild -scheme MyApp -configuration Debug build"},
		{"tsc", "tsc --noEmit"},
		{"eslint", "eslint src/"},
		{"prettier", "prettier --check ."},
		{"black", "black --check ."},
		{"isort", "isort --check ."},
		{"flake8", "flake8 src/"},
		{"mypy", "mypy src/"},
		{"pylint", "pylint src/"},
		{"ruff", "ruff check ."},
		{"gofmt -w", "gofmt -w file.go"},
		{"gofmt -l", "gofmt -l ./..."},
		{"goimports -w", "goimports -w file.go"},
		{"shellcheck", "shellcheck script.sh"},
	}

	for _, tt := range alwaysSafe {
		t.Run(tt.name, func(t *testing.T) {
			cmdName := strings.Fields(tt.cmd)[0]
			if got := evalCmd(t, cmdName, tt.cmd); !got {
				t.Errorf("expected ALLOW for %q", tt.cmd)
			}
		})
	}
}

// --------------------------------------------------------------------------- sort
// ---------------------------------------------------------------------------

func TestSort(t *testing.T) {
	tests := []struct {
		name  string
		cmd   string
		allow bool
	}{
		{"bare sort", "sort file.txt", true},
		{"sort -r", "sort -r file.txt", true},
		{"sort -o project", "sort -o /project/out.txt data.txt", true},
		{"sort --output project", "sort --output /project/out.txt data.txt", true},
		{"sort --output= tmp", "sort --output=/tmp/out.txt data.txt", true},
		{"sort -o /tmp", "sort -o /tmp/out.txt data.txt", true},
		{"sort -o denied", "sort -o /etc/out.txt data.txt", false},
		{"sort --output denied", "sort --output /etc/out.txt data.txt", false},
		{"sort --output= denied", "sort --output=/etc/out.txt data.txt", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := evalCmd(t, "sort", tt.cmd); got != tt.allow {
				t.Errorf("sort %q = %v, want %v", tt.cmd, got, tt.allow)
			}
		})
	}
}

// --------------------------------------------------------------------------- yq
// ---------------------------------------------------------------------------

func TestYq(t *testing.T) {
	tests := []struct {
		name  string
		cmd   string
		allow bool
	}{
		{"read", "yq '.foo' file.yaml", true},
		{"eval", "yq eval '.bar' file.yaml", true},
		{"-i denied", "yq -i '.foo' file.yaml", false},
		{"--in-place denied", "yq --in-place '.foo' file.yaml", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := evalCmd(t, "yq", tt.cmd); got != tt.allow {
				t.Errorf("yq %q = %v, want %v", tt.cmd, got, tt.allow)
			}
		})
	}
}

// --------------------------------------------------------------------------- env
// ---------------------------------------------------------------------------

func TestEnv(t *testing.T) {
	tests := []struct {
		name  string
		cmd   string
		allow bool
	}{
		{"bare env", "env", true},
		{"with var", "env FOO=bar", false},
		{"with -i", "env -i", false},
		{"env command", "env ls", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := evalCmd(t, "env", tt.cmd); got != tt.allow {
				t.Errorf("env %q = %v, want %v", tt.cmd, got, tt.allow)
			}
		})
	}
}

// --------------------------------------------------------------------------- command
// ---------------------------------------------------------------------------

func TestCommand(t *testing.T) {
	tests := []struct {
		name  string
		cmd   string
		allow bool
	}{
		{"-v git", "command -v git", true},
		{"-V ls", "command -V ls", true},
		{"bare command git", "command git", false},
		{"-p git", "command -p git", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := evalCmd(t, "command", tt.cmd); got != tt.allow {
				t.Errorf("command %q = %v, want %v", tt.cmd, got, tt.allow)
			}
		})
	}
}

// --------------------------------------------------------------------------- Subcommand allowlist commands
// ---------------------------------------------------------------------------

func TestGh(t *testing.T) {
	tests := []struct {
		name  string
		cmd   string
		allow bool
	}{
		// Top-level passthrough.
		{"status", "gh status", true},
		{"browse", "gh browse", true},
		{"search", "gh search prs --author=me", true},
		{"version", "gh version", true},

		// Read-only sub-actions.
		{"auth status", "gh auth status", true},
		{"pr view", "gh pr view 123", true},
		{"pr list", "gh pr list", true},
		{"pr diff", "gh pr diff 123", true},
		{"pr checkout", "gh pr checkout 123", true},
		{"issue view", "gh issue view 456", true},
		{"issue list", "gh issue list", true},
		{"repo view", "gh repo view", true},
		{"repo list", "gh repo list", true},
		{"run list", "gh run list", true},
		{"run view", "gh run view 789", true},
		{"workflow list", "gh workflow list", true},
		{"workflow view", "gh workflow view foo.yml", true},
		{"release view", "gh release view v1.0", true},
		{"release list", "gh release list", true},
		{"gist view", "gh gist view abc", true},
		{"gist list", "gh gist list", true},
		{"codespace list", "gh codespace list", true},
		{"label list", "gh label list", true},
		{"project list", "gh project list", true},
		{"config list", "gh config list", true},
		{"config get", "gh config get editor", true},

		// Mutating ops fall through (require manual approval).
		{"issue create", "gh issue create --title foo", false},
		{"repo delete", "gh repo delete myorg/repo --yes", false},
		{"release create", "gh release create v1.0 --target main", false},
		{"release upload", "gh release upload v1.0 /tmp/secrets.tar", false},
		{"workflow run", "gh workflow run prod-deploy.yml", false},
		{"auth login", "gh auth login", false},

		// `gh api` is the generic HTTP escape hatch — never auto-allow.
		{"api GET", "gh api /repos", false},
		{"api DELETE", "gh api -X DELETE /repos/foo/bar", false},
		{"api graphql mutation", `gh api graphql -f query='mutation { ... }'`, false},

		// `gh dbx` is dropped from the allowlist — fall through.
		{"dbx pr", "gh dbx pr --apply-patches", false},

		// Unknown subcommand still falls through.
		{"unknown sub", "gh evil-command", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := evalCmd(t, "gh", tt.cmd); got != tt.allow {
				t.Errorf("gh %q = %v, want %v", tt.cmd, got, tt.allow)
			}
		})
	}
}

func TestCargo(t *testing.T) {
	tests := []struct {
		name  string
		cmd   string
		allow bool
	}{
		{"build", "cargo build", true},
		{"test verbose", "cargo test -v", true},
		{"clippy", "cargo clippy -- -D warnings", true},
		{"fmt", "cargo fmt", true},
		{"unknown", "cargo evil-sub", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := evalCmd(t, "cargo", tt.cmd); got != tt.allow {
				t.Errorf("cargo %q = %v, want %v", tt.cmd, got, tt.allow)
			}
		})
	}
}

func TestNpm(t *testing.T) {
	tests := []struct {
		name  string
		cmd   string
		allow bool
	}{
		{"install", "npm install", true},
		{"test", "npm test", true},
		{"run build", "npm run build", true},
		{"ci", "npm ci", true},
		{"audit", "npm audit", true},
		{"unknown", "npm evil-sub", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := evalCmd(t, "npm", tt.cmd); got != tt.allow {
				t.Errorf("npm %q = %v, want %v", tt.cmd, got, tt.allow)
			}
		})
	}
}

func TestYarn(t *testing.T) {
	tests := []struct {
		name  string
		cmd   string
		allow bool
	}{
		{"install", "yarn install", true},
		{"add pkg", "yarn add react", true},
		{"build", "yarn build", true},
		{"workspace", "yarn workspace pkg build", true},
		{"unknown", "yarn evil-sub", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := evalCmd(t, "yarn", tt.cmd); got != tt.allow {
				t.Errorf("yarn %q = %v, want %v", tt.cmd, got, tt.allow)
			}
		})
	}
}

func TestPnpm(t *testing.T) {
	tests := []struct {
		name  string
		cmd   string
		allow bool
	}{
		{"install", "pnpm install", true},
		{"run test", "pnpm run test", true},
		{"store", "pnpm store prune", true},
		{"unknown", "pnpm evil-sub", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := evalCmd(t, "pnpm", tt.cmd); got != tt.allow {
				t.Errorf("pnpm %q = %v, want %v", tt.cmd, got, tt.allow)
			}
		})
	}
}

func TestBun(t *testing.T) {
	tests := []struct {
		name  string
		cmd   string
		allow bool
	}{
		{"install", "bun install", true},
		{"run test", "bun run test", true},
		{"test", "bun test", true},
		{"pm", "bun pm ls", true},
		{"unknown", "bun evil-sub", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := evalCmd(t, "bun", tt.cmd); got != tt.allow {
				t.Errorf("bun %q = %v, want %v", tt.cmd, got, tt.allow)
			}
		})
	}
}

func TestPip(t *testing.T) {
	tests := []struct {
		name  string
		cmd   string
		allow bool
	}{
		{"install", "pip install requests", true},
		{"list", "pip list", true},
		{"freeze", "pip freeze", true},
		{"unknown", "pip evil-sub", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := evalCmd(t, "pip", tt.cmd); got != tt.allow {
				t.Errorf("pip %q = %v, want %v", tt.cmd, got, tt.allow)
			}
		})
	}
}

func TestPip3(t *testing.T) {
	tests := []struct {
		name  string
		cmd   string
		allow bool
	}{
		{"install", "pip3 install flask", true},
		{"show", "pip3 show flask", true},
		{"unknown", "pip3 evil-sub", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := evalCmd(t, "pip3", tt.cmd); got != tt.allow {
				t.Errorf("pip3 %q = %v, want %v", tt.cmd, got, tt.allow)
			}
		})
	}
}

// --------------------------------------------------------------------------- Path write commands (cp, mv, rm, mkdir,
// touch, tee) ---------------------------------------------------------------------------

func TestPathWriteCommands(t *testing.T) {
	tests := []struct {
		name  string
		cmd   string
		allow bool
	}{
		// cp
		{"cp to project", "cp src.txt /project/dest.txt", true},
		{"cp to tmp", "cp src.txt /tmp/backup", true},
		{"cp outside", "cp src.txt /etc/dest", false},
		{"cp -t /tmp", "cp -t /tmp file.txt", true},
		{"cp -t /etc", "cp -t /etc file.txt", false},
		{"cp --target-directory /project", "cp --target-directory /project/dir file.txt", true},
		{"cp --target-directory= /tmp", "cp --target-directory=/tmp file.txt", true},
		{"cp --target-directory /etc", "cp --target-directory /etc file.txt", false},

		// mv
		{"mv to project", "mv old.txt /project/new.txt", true},
		{"mv outside", "mv old.txt /etc/evil", false},
		{"mv -t /tmp", "mv -t /tmp file.txt", true},
		{"mv -t /etc", "mv -t /etc file.txt", false},

		// rm
		{"rm in project", "rm /project/junk.txt", true},
		{"rm in tmp", "rm /tmp/junk", true},
		{"rm outside", "rm /etc/important", false},
		{"rm -rf project", "rm -rf /project/build", true},

		// mkdir
		{"mkdir in project", "mkdir /project/newdir", true},
		{"mkdir -p in tmp", "mkdir -p /tmp/a/b/c", true},
		{"mkdir outside", "mkdir /etc/evil", false},

		// touch
		{"touch in project", "touch /project/file.txt", true},
		{"touch relative", "touch file.txt", true},
		{"touch outside", "touch /etc/evil", false},

		// tee
		{"tee to project", "tee /project/output.log", true},
		{"tee to tmp", "tee /tmp/out", true},
		{"tee outside", "tee /etc/evil", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmdName := strings.Fields(tt.cmd)[0]
			if got := evalCmd(t, cmdName, tt.cmd); got != tt.allow {
				t.Errorf("%q = %v, want %v", tt.cmd, got, tt.allow)
			}
		})
	}
}

// --------------------------------------------------------------------------- find
// ---------------------------------------------------------------------------

func TestFind(t *testing.T) {
	tests := []struct {
		name  string
		cmd   string
		allow bool
	}{
		{"name and print", "find . -name '*.go' -print", true},
		{"type and maxdepth", "find . -type f -maxdepth 3", true},
		{"print0", "find /project -name '*.go' -print0", true},
		{"printf format", "find . -name '*.go' -printf '%p\\n'", true},
		{"prune and or", "find . -name .git -prune -or -name '*.go' -print", true},
		{"mtime and size", "find . -mtime -7 -size +1M", true},
		{"regex", "find . -regex '.*\\.go$'", true},
		{"not and empty", "find . -not -empty -type f", true},
		{"exec denied", `find . -exec rm {} \;`, false},
		{"execdir denied", `find . -execdir cat {} \;`, false},
		{"delete denied", "find . -name '*.tmp' -delete", false},
		{"fprint denied", "find . -fprint /etc/evil", false},
		{"ok denied", `find . -ok rm {} \;`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := evalCmd(t, "find", tt.cmd); got != tt.allow {
				t.Errorf("find %q = %v, want %v", tt.cmd, got, tt.allow)
			}
		})
	}
}

// --------------------------------------------------------------------------- fd / fdfind
// ---------------------------------------------------------------------------

func TestFd(t *testing.T) {
	tests := []struct {
		name  string
		cmd   string
		allow bool
	}{
		{"simple pattern", "fd pattern", true},
		{"type filter", "fd -t f pattern", true},
		{"extension and hidden", "fd -e go -H", true},
		{"max-depth=", "fd --max-depth=3 pattern", true},
		{"case-sensitive", "fd -s pattern", true},
		{"follow symlinks", "fd -L pattern", true},
		{"glob mode", "fd -g '*.go'", true},
		{"exec denied", "fd -x rm", false},
		{"long exec denied", "fd --exec rm", false},
		{"exec-batch denied", "fd --exec-batch cat", false},
		{"unknown flag denied", "fd --unknown pattern", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := evalCmd(t, "fd", tt.cmd); got != tt.allow {
				t.Errorf("fd %q = %v, want %v", tt.cmd, got, tt.allow)
			}
		})
	}
}

func TestFdfind(t *testing.T) {
	tests := []struct {
		name  string
		cmd   string
		allow bool
	}{
		{"simple", "fdfind pattern", true},
		{"exec denied", "fdfind --exec rm", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := evalCmd(t, "fdfind", tt.cmd); got != tt.allow {
				t.Errorf("fdfind %q = %v, want %v", tt.cmd, got, tt.allow)
			}
		})
	}
}

// --------------------------------------------------------------------------- git
// ---------------------------------------------------------------------------

func TestGit(t *testing.T) {
	tests := []struct {
		name  string
		cmd   string
		allow bool
	}{
		{"status", "git status", true},
		{"log --oneline", "git log --oneline", true},
		{"diff", "git diff HEAD~1", true},
		{"show", "git show HEAD", true},
		{"branch -a", "git branch -a", true},
		{"tag -l", "git tag -l", true},
		{"add file", "git add file.go", true},
		{"commit -m", `git commit -m "msg"`, true},
		{"push", "git push", true},
		{"pull", "git pull origin main", true},
		{"fetch", "git fetch --all", true},
		{"checkout branch", "git checkout main", true},
		{"switch branch", "git switch feature", true},
		{"stash", "git stash", true},
		{"merge branch", "git merge feature", true},
		{"rebase main", "git rebase main", true},
		{"remote -v", "git remote -v", true},
		{"rev-parse HEAD", "git rev-parse HEAD", true},
		{"ls-files", "git ls-files", true},
		{"blame file", "git blame file.go", true},
		{"cherry-pick", "git cherry-pick abc123", true},
		{"clean", "git clean -fd", true},
		{"reset", "git reset HEAD~1", true},
		{"worktree list", "git worktree list", true},
		{"submodule status", "git submodule status", true},
		{"submodule summary", "git submodule summary", true},
		{"clone", "git clone https://github.com/example/repo.git", true},

		// Denied: -c flag (space-separated so parser sees -c as a flag)
		{"-c flag denied", "git -c core.editor=evil status", false},
		// --config-env with space so it's parsed as a standalone flag
		{"--config-env denied", "git --config-env GIT_FOO=BAR status", false},

		// Denied: double dash
		{"double dash denied", "git -- file.txt", false},

		// Denied: unknown subcommand
		{"unknown subcommand", "git evil-subcommand", false},
		{"bare git", "git", false},

		// Denied: `git config` (code-execution vector via aliases / core.hooksPath / etc.)
		{"config --get", "git config --get user.name", false},
		{"config --list", "git config --list", false},
		{"config alias bang", `git config alias.evil "!rm -rf /"`, false},
		{"config core.hooksPath", "git config core.hooksPath /tmp/hooks", false},

		// Denied: `git submodule` mutating ops (clone arbitrary repo / run hooks / run arbitrary cmd).
		{"submodule add", "git submodule add https://github.com/foo/bar", false},
		{"submodule update", "git submodule update --init --recursive", false},
		{"submodule foreach", `git submodule foreach 'echo hi'`, false},
		{"submodule deinit", "git submodule deinit foo", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := evalCmd(t, "git", tt.cmd); got != tt.allow {
				t.Errorf("git %q = %v, want %v", tt.cmd, got, tt.allow)
			}
		})
	}
}

// --------------------------------------------------------------------------- go
// ---------------------------------------------------------------------------

func TestGo(t *testing.T) {
	tests := []struct {
		name  string
		cmd   string
		allow bool
	}{
		{"build", "go build ./...", true},
		{"test -v", "go test -v ./...", true},
		{"vet", "go vet ./...", true},
		{"fmt", "go fmt ./...", true},
		{"mod tidy", "go mod tidy", true},
		{"get package", "go get golang.org/x/tools", true},
		{"install", "go install golang.org/x/tools/gopls@latest", true},
		{"env", "go env GOPATH", true},
		{"version", "go version", true},
		{"generate", "go generate ./...", true},
		{"doc", "go doc fmt.Println", true},

		// go run with path checks
		{"run dot", "go run .", true},
		{"run relative", "go run ./cmd/server", true},
		{"run file", "go run main.go", true},
		{"run project abs", "go run /project/main.go", true},
		{"run /etc denied", "go run /etc/backdoor.go", false},

		// Unknown subcommand
		{"unknown subcommand", "go evil-sub", false},
		{"bare go", "go", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := evalCmd(t, "go", tt.cmd); got != tt.allow {
				t.Errorf("go %q = %v, want %v", tt.cmd, got, tt.allow)
			}
		})
	}
}

// --------------------------------------------------------------------------- ssh
//
// The rule engine's every_positional_passes with ssh_remote_eval checks each positional as a potential host. Multi-word
// remote commands must be quoted into a single shell word so the host is the only positional that matters.
// ---------------------------------------------------------------------------

func TestSsh(t *testing.T) {
	tests := []struct {
		name  string
		cmd   string
		allow bool
	}{
		// Single-word remote command (no quoting needed)
		{"single word cmd", "ssh allowed.example.com ls", true},
		{"-p port", "ssh -p 22 allowed.example.com ls", true},

		// Multi-word remote commands require quoting
		{"quoted remote cmd", `ssh -t allowed.example.com "git status"`, true},
		{"-i key quoted", `ssh -i /path/to/key allowed.example.com "git log"`, true},
		{"-4 flag quoted", `ssh -4 allowed.example.com "git diff"`, true},
		{"combined -tn quoted", `ssh -tn allowed.example.com "git status"`, true},

		// Unknown flags denied
		{"unknown flag -X", `ssh -X allowed.example.com "git status"`, false},

		// No remote command denied (ssh_remote_eval requires non-empty remote cmd)
		{"no remote cmd", "ssh allowed.example.com", false},

		// Unknown host denied
		{"unknown host", `ssh unknown.host "git status"`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := evalCmd(t, "ssh", tt.cmd); got != tt.allow {
				t.Errorf("ssh %q = %v, want %v", tt.cmd, got, tt.allow)
			}
		})
	}
}

// --------------------------------------------------------------------------- tsh (delegates to ssh spec via
// ref_command_spec) ---------------------------------------------------------------------------

func TestTsh(t *testing.T) {
	tests := []struct {
		name  string
		cmd   string
		allow bool
	}{
		{"tsh ssh single cmd", "tsh ssh allowed.example.com ls", true},
		{"tsh ssh quoted cmd", `tsh ssh allowed.example.com "git status"`, true},
		{"tsh ssh unknown host", `tsh ssh evil.host "git status"`, false},
		{"tsh ssh no cmd", "tsh ssh allowed.example.com", false},
		{"tsh non-ssh sub", "tsh proxy app", false},
		{"bare tsh", "tsh", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := evalCmd(t, "tsh", tt.cmd); got != tt.allow {
				t.Errorf("tsh %q = %v, want %v", tt.cmd, got, tt.allow)
			}
		})
	}
}

// --------------------------------------------------------------------------- sed (builtin checker resolved via
// EvalCtx.Checkers) ---------------------------------------------------------------------------

func TestSed(t *testing.T) {
	tests := []struct {
		name  string
		cmd   string
		allow bool
	}{
		{"simple subst", "sed 's/foo/bar/g'", true},
		{"delete pattern", "sed '/pattern/d'", true},
		{"multiple -e", "sed -e 's/a/b/' -e '/x/d'", true},
		{"print", "sed -n '/foo/p'", true},
		{"address range", "sed '1,10d'", true},
		{"translate", "sed 'y/abc/xyz/'", true},
		{"hold space", "sed -n 'h;n;H;g;p'", true},

		// Dangerous
		{"e flag on subst", "sed 's/foo/bar/e'", false},
		{"w command", "sed 'w /etc/evil'", false},
		{"e command", "sed 'e'", false},
		{"-i flag", "sed -i 's/foo/bar/' file", false},
		{"--in-place", "sed --in-place 's/foo/bar/' file", false},
		{"-f script", "sed -f script.sed file", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := evalCmd(t, "sed", tt.cmd); got != tt.allow {
				t.Errorf("sed %q = %v, want %v", tt.cmd, got, tt.allow)
			}
		})
	}
}

func TestGsed(t *testing.T) {
	tests := []struct {
		name  string
		cmd   string
		allow bool
	}{
		{"simple subst", "gsed 's/foo/bar/'", true},
		{"-i denied", "gsed -i 's/foo/bar/' file", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := evalCmd(t, "gsed", tt.cmd); got != tt.allow {
				t.Errorf("gsed %q = %v, want %v", tt.cmd, got, tt.allow)
			}
		})
	}
}

// --------------------------------------------------------------------------- awk (builtin checker resolved via
// EvalCtx.Checkers) ---------------------------------------------------------------------------

func TestAwk(t *testing.T) {
	tests := []struct {
		name  string
		cmd   string
		allow bool
	}{
		{"simple print", "awk '{print $1}'", true},
		{"field separator", "awk -F: '{print $1}'", true},
		{"comparison", "awk '$3 > 100' file.txt", true},
		{"with -v", "awk -v x=1 '{print x}'", true},

		// Dangerous
		{"redirect", `awk '{print > "/tmp/out"}'`, false},
		{"append", `awk '{print >> "file"}'`, false},
		{"pipe out", `awk '{print | "cmd"}'`, false},
		{"system call", `awk '{system("ls")}'`, false},
		{"getline", "awk '{getline line}'", false},
		{"-f script", "awk -f script.awk", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := evalCmd(t, "awk", tt.cmd); got != tt.allow {
				t.Errorf("awk %q = %v, want %v", tt.cmd, got, tt.allow)
			}
		})
	}
}

func TestGawk(t *testing.T) {
	tests := []struct {
		name  string
		cmd   string
		allow bool
	}{
		{"simple", "gawk '{print $1}'", true},
		{"system denied", `gawk '{system("ls")}'`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := evalCmd(t, "gawk", tt.cmd); got != tt.allow {
				t.Errorf("gawk %q = %v, want %v", tt.cmd, got, tt.allow)
			}
		})
	}
}

// --------------------------------------------------------------------------- Verify all commands in default.txtpb have
// a corresponding spec ---------------------------------------------------------------------------

func TestAllDefaultCommandsResolvable(t *testing.T) {
	rs, err := DefaultRules()
	if err != nil {
		t.Fatalf("DefaultRules: %v", err)
	}

	for _, spec := range rs.GetCommands() {
		for _, name := range spec.GetNames() {
			if LookupCommand(rs, name) == nil {
				t.Errorf("LookupCommand(%q) = nil", name)
			}
		}
	}
}

func TestTerraform(t *testing.T) {
	tests := []struct {
		cmd   string
		allow bool
	}{
		{"terraform init", true},
		{"terraform validate", true},
		{"terraform plan", true},
		{"terraform plan -out=plan.tfplan", true},
		{"terraform fmt", true},
		{"terraform fmt -check", true},
		{"terraform show", true},
		{"terraform output", true},
		{"terraform output -json", true},
		{"terraform version", true},
		{"terraform providers", true},
		{"terraform graph", true},
		{"terraform state list", true},
		{"terraform state show aws_instance.example", true},
		{"terraform workspace list", true},
		{"terraform workspace show", true},
		{"terraform workspace select dev", true},
		// Dangerous — fall through
		{"terraform apply", false},
		{"terraform apply plan.tfplan", false},
		{"terraform destroy", false},
		{"terraform import aws_instance.example i-1234567890", false},
		{"terraform refresh", false},
		{"terraform taint aws_instance.example", false},
		{"terraform untaint aws_instance.example", false},
		{"terraform state rm aws_instance.example", false},
		{"terraform state mv aws_instance.a aws_instance.b", false},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			if got := evalCmd(t, "terraform", tt.cmd); got != tt.allow {
				t.Errorf("evalCmd(%q) = %v, want %v", tt.cmd, got, tt.allow)
			}
		})
	}
}

func TestTerragrunt(t *testing.T) {
	tests := []struct {
		cmd   string
		allow bool
	}{
		// OpenTofu/Terraform pass-through (safe subset)
		{"terragrunt init", true},
		{"terragrunt validate", true},
		{"terragrunt plan", true},
		{"terragrunt plan -out=plan.tfplan", true},
		{"terragrunt fmt", true},
		{"terragrunt show", true},
		{"terragrunt output", true},
		{"terragrunt output -json", true},
		{"terragrunt version", true},
		{"terragrunt providers", true},
		{"terragrunt graph", true},
		// Terragrunt-native safe commands
		{"terragrunt catalog", true},
		{"terragrunt scaffold", true},
		{"terragrunt find", true},
		{"terragrunt list", true},
		{"terragrunt render", true},
		{"terragrunt render --json", true},
		// Multi-word subcommands
		{"terragrunt hcl fmt", true},
		{"terragrunt hcl fmt --check", true},
		{"terragrunt hcl validate", true},
		{"terragrunt dag graph", true},
		{"terragrunt info print", true},
		{"terragrunt info strict", true},
		// State/workspace reads
		{"terragrunt state list", true},
		{"terragrunt state show aws_instance.example", true},
		{"terragrunt workspace list", true},
		{"terragrunt workspace show", true},
		{"terragrunt workspace select dev", true},
		// Stack safe ops
		{"terragrunt stack output", true},
		{"terragrunt stack generate", true},
		// Dangerous — fall through
		{"terragrunt apply", false},
		{"terragrunt apply plan.tfplan", false},
		{"terragrunt destroy", false},
		{"terragrunt import aws_instance.example i-1234567890", false},
		{"terragrunt refresh", false},
		{"terragrunt taint aws_instance.example", false},
		{"terragrunt untaint aws_instance.example", false},
		{"terragrunt state rm aws_instance.example", false},
		{"terragrunt state mv aws_instance.a aws_instance.b", false},
		{"terragrunt stack run plan", false},
		{"terragrunt stack run apply", false},
		{"terragrunt stack clean", false},
		{"terragrunt run plan", false},
		{"terragrunt run apply", false},
		{"terragrunt exec echo hello", false},
		{"terragrunt backend bootstrap", false},
		{"terragrunt backend delete", false},
		{"terragrunt backend migrate", false},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			if got := evalCmd(t, "terragrunt", tt.cmd); got != tt.allow {
				t.Errorf("evalCmd(%q) = %v, want %v", tt.cmd, got, tt.allow)
			}
		})
	}
}

func TestAws(t *testing.T) {
	tests := []struct {
		cmd   string
		allow bool
	}{
		{"aws sts get-caller-identity", true},
		{"aws sts get-session-token", true},
		{"aws s3 ls", true},
		{"aws s3 ls s3://my-bucket", true},
		{"aws s3api list-buckets", true},
		{"aws s3api list-objects --bucket my-bucket", true},
		{"aws s3api head-object --bucket b --key k", true},
		{"aws ec2 describe-instances", true},
		{"aws ec2 describe-instances --instance-ids i-123", true},
		{"aws ec2 describe-vpcs", true},
		{"aws ec2 describe-regions", true},
		{"aws iam get-user", true},
		{"aws iam list-roles", true},
		{"aws iam list-policies --scope Local", true},
		{"aws lambda list-functions", true},
		{"aws lambda get-function --function-name my-func", true},
		{"aws cloudformation describe-stacks", true},
		{"aws cloudformation list-stacks", true},
		{"aws cloudformation validate-template --template-body file://t.yaml", true},
		{"aws logs describe-log-groups", true},
		{"aws logs filter-log-events --log-group-name my-group", true},
		{"aws configure list", true},
		{"aws configure get region", true},
		// Dangerous — fall through
		{"aws s3 cp file.txt s3://bucket/key", false},
		{"aws s3 rm s3://bucket/key", false},
		{"aws s3 sync . s3://bucket", false},
		{"aws ec2 run-instances --image-id ami-123", false},
		{"aws ec2 terminate-instances --instance-ids i-123", false},
		{"aws ec2 create-vpc --cidr-block 10.0.0.0/16", false},
		{"aws iam create-user --user-name evil", false},
		{"aws iam delete-role --role-name myrole", false},
		{"aws lambda create-function --function-name f", false},
		{"aws lambda update-function-code --function-name f", false},
		{"aws cloudformation create-stack --stack-name s", false},
		{"aws cloudformation delete-stack --stack-name s", false},
		{"aws logs delete-log-group --log-group-name g", false},
		{"aws dynamodb put-item --table-name t", false},
		{"aws unknown-service some-operation", false},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			if got := evalCmd(t, "aws", tt.cmd); got != tt.allow {
				t.Errorf("evalCmd(%q) = %v, want %v", tt.cmd, got, tt.allow)
			}
		})
	}
}

func TestBuf(t *testing.T) {
	tests := []struct {
		cmd   string
		allow bool
	}{
		{"buf generate", true},
		{"buf generate --template buf.gen.yaml", true},
		{"buf build", true},
		{"buf lint", true},
		{"buf lint proto/", true},
		{"buf format", true},
		{"buf format -w", true},
		{"buf breaking --against .git#branch=main", true},
		{"buf version", true},
		{"buf mod init", true},
		{"buf mod update", true},
		{"buf dep update", true},
		// Dangerous — fall through
		{"buf push", false},
		{"buf push buf.build/owner/repo", false},
		{"buf unknown-subcommand", false},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			if got := evalCmd(t, "buf", tt.cmd); got != tt.allow {
				t.Errorf("evalCmd(%q) = %v, want %v", tt.cmd, got, tt.allow)
			}
		})
	}
}

func TestProtoc(t *testing.T) {
	tests := []struct {
		cmd   string
		allow bool
	}{
		{"protoc --version", true},
		{"protoc --go_out=. foo.proto", true},
		{"protoc -I proto --go_out=gen proto/foo.proto", true},
		{"protoc --descriptor_set_out=desc.pb foo.proto", true},
		{"protoc --help", true},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			if got := evalCmd(t, "protoc", tt.cmd); got != tt.allow {
				t.Errorf("evalCmd(%q) = %v, want %v", tt.cmd, got, tt.allow)
			}
		})
	}
}
