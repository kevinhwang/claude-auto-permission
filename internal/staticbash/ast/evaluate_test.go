package ast

import (
	"os"
	"testing"

	"claude-auto-permission/internal/config"
	configpb "claude-auto-permission/internal/gen/config/v1"
	rulespb "claude-auto-permission/internal/gen/rules/v1"
	"claude-auto-permission/internal/staticbash/builtins"
	"claude-auto-permission/internal/staticbash/rules"
)

const testCwd = "/project"

var (
	testRuleSet  *rulespb.RuleSet
	testCheckers = builtins.DefaultRegistry()
	testCfg      *config.Resolver
)

// evalCmd is a thin convenience wrapper around [Evaluate] used by the table-driven tests below. Returns just the
// boolean verdict — tests that need `Matched` build the [Input] inline.
func evalCmd(cmd, cwd, projectRoot string) bool {
	return Evaluate(Input{
		Command:     cmd,
		Cwd:         cwd,
		ProjectRoot: projectRoot,
		RuleSet:     testRuleSet,
		Checkers:    testCheckers,
		Config:      testCfg,
	}).Allowed
}

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
						HostPatterns:       []string{"test.example.com"},
						AllowWritePatterns: []string{"/tmp/**"},
					}.Build()},
				}.Build(),
			}.Build(),
		},
	}.Build())
	var err error
	testRuleSet, err = rules.DefaultRules()
	if err != nil {
		panic(err)
	}
	os.Exit(m.Run())
}

func TestEvaluate_Allow(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
	}{
		{name: "simple git status", cmd: "git status"},
		{name: "ls with flags", cmd: "ls -la"},
		{name: "echo string", cmd: `echo "hello world"`},
		{name: "cat file", cmd: "cat file.txt"},
		{name: "grep pattern", cmd: "grep -r pattern src/"},
		{name: "cd and git", cmd: "cd dir && git status"},
		{name: "cd and agent-bzl", cmd: "cd .claude/worktrees/wt && agent-bzl test //pkg:test --test_output=streamed"},
		{name: "triple &&", cmd: "ls -la && pwd && echo hello"},
		{name: "git add and commit", cmd: "git add . && git commit -m 'fix bug'"},
		{name: "or fallback", cmd: "git pull || echo failed"},
		{name: "pipe git to head", cmd: "git log | head -20"},
		{name: "triple pipe", cmd: "cat file.txt | grep pattern | wc -l"},
		{name: "git log with redirect and pipe", cmd: "git log 2>&1 | head"},
		{name: "semicolon", cmd: "echo hello; echo world"},
		{name: "cd then pipe", cmd: "cd dir && git log | head -20"},
		{name: "complex chain", cmd: "cd dir && npm install && npm test"},
		{name: "subshell", cmd: "(cd dir && git status)"},
		{name: "nested subshell", cmd: "(cd dir && (echo a; echo b))"},
		{name: "block", cmd: "{ echo a; echo b; }"},
		{name: "if then fi", cmd: "if git diff --quiet; then echo clean; fi"},
		{name: "if then else fi", cmd: "if git diff --quiet; then echo clean; else echo dirty; fi"},
		{name: "if elif else", cmd: "if [ -f a ]; then cat a; elif [ -f b ]; then cat b; else echo none; fi"},
		{name: "for in do done", cmd: "for f in *.txt; do cat \"$f\"; done"},
		{name: "for with echo", cmd: "for i in 1 2 3; do echo $i; done"},
		{name: "while do done", cmd: "while ! grep -q ready status.txt; do sleep 1; done"},
		{name: "case", cmd: `case "$1" in *.txt) cat "$1";; *.md) head "$1";; esac`},
		{name: "case with subst word", cmd: `case "$(echo txt)" in *.txt) echo match;; esac`},
		{name: "test bracket", cmd: "[[ -f file.txt ]] && cat file.txt"},
		{name: "test with var", cmd: `[[ "$FOO" == "bar" ]] && echo yes`},
		{name: "cmd subst in arg", cmd: `echo "branch: $(git rev-parse --abbrev-ref HEAD)"`},
		{name: "cmd subst in arg 2", cmd: `echo "hash: $(git log -1 --format='%H')"`},
		{name: "nested safe subst", cmd: `echo "$(echo hello)"`},
		{name: "process subst", cmd: "diff <(git show HEAD:file) <(cat file)"},
		{name: "negation", cmd: "! git diff --quiet"},
		{name: "redirect to /dev/null", cmd: "git log > /dev/null"},
		{name: "stderr to /dev/null", cmd: "git log 2>/dev/null"},
		{name: "fd redirect", cmd: "git log 2>&1"},
		{name: "redirect to project dir", cmd: "git log > output.txt"},
		{name: "redirect to /tmp", cmd: "echo data > /tmp/file.txt"},
		{name: "redirect to /dev/null compound", cmd: "git log > /dev/null && echo done"},
		{name: "n> redirect safe", cmd: "echo foo 1>/dev/null"},
		// All env-var assignment forms — bare, prefix, DeclClause — fall through. See FallThrough block. git -C is an unknown
		// flag (skipped by default); the subcommand is still located and matched.
		{name: "git -C", cmd: "git -C /path status"},
		{name: "git -C with --no-pager", cmd: "git -C /path --no-pager log"},
		{name: "git --no-pager log", cmd: "git --no-pager log --oneline"},
		// `git config` is intentionally rejected — see default.txtpb. Read forms still need user approval; mutation forms are
		// arbitrary code execution via aliases / core.hooksPath.
		{name: "ssh simple", cmd: "ssh test.example.com -- git status"},
		{name: "ssh no dashdash", cmd: "ssh test.example.com git status"},
		{name: "ssh with -p", cmd: "ssh -p 22 test.example.com -- git status"},
		{name: "ssh with -i", cmd: "ssh -i ~/.ssh/key test.example.com git log"},
		{name: "ssh with -tt", cmd: "ssh -tt test.example.com git status"},
		{name: "ssh quoted compound", cmd: `ssh test.example.com -- "cd /path && git status"`},
		{name: "tsh ssh simple", cmd: "tsh ssh test.example.com git status"},
		{name: "tsh ssh with flags", cmd: "tsh ssh -p 22 test.example.com -- git log"},
		{name: "npm install", cmd: "npm install"},
		{name: "npm test", cmd: "cd dir && npm test"},
		{name: "npm run build", cmd: "npm run build"},
		{name: "cargo build", cmd: "cd dir && cargo build"},
		{name: "go test", cmd: "cd dir && go test ./..."},
		{name: "pip install", cmd: "pip install -r requirements.txt"},
		{name: "bun install and test", cmd: "cd dir && bun install && bun test"},
		{name: "yarn install and build", cmd: "cd dir && yarn install && yarn build"},
		{name: "go run dot", cmd: "go run ."},
		{name: "go run relative", cmd: "go run ./cmd/server"},
		{name: "go run file", cmd: "go run main.go"},
		{name: "go run with flags", cmd: "go run -race ./cmd/server"},
		{name: "go run with -- args", cmd: "go run ./cmd/server -- --port=8080"},
		{name: "mkdir tmp", cmd: "mkdir -p /tmp/workdir"},
		{name: "touch tmp", cmd: "touch /tmp/marker.txt"},
		{name: "cp to tmp", cmd: "cp config.json /tmp/backup.json"},
		{name: "tee to tmp", cmd: "echo data | tee /tmp/log.txt"},
		{name: "cp target-dir tmp", cmd: "cp --target-directory=/tmp file.txt"},
		// DeclClause forms (declare, local, readonly) are not supported — they fall through. See FallThrough block.
		{name: "arithm cmd", cmd: "(( x = 1 + 2 ))"},
		{name: "c-style for", cmd: "for ((i=0; i<10; i++)); do echo $i; done"},
		{name: "time cmd", cmd: "time git status"},
		{name: "let expr", cmd: "let x=1+2"},
		{name: "here-string", cmd: "cat <<< 'hello world'"},
		{name: "command -v", cmd: "command -v git"},
		{name: "command -V", cmd: "command -V git"},
		// command: only -v/-V are allowed here. Bare `command` (without -v/-V) and `env` with args fall through — see the
		// FallThrough block.
		{name: "bare env", cmd: "env"},
		{name: "awk print", cmd: `awk '{print $1}' file.txt`},
		{name: "awk with -F", cmd: `awk -F: '{print $1}' file.txt`},
		{name: "awk with -v", cmd: `awk -v OFS='\t' '{print $1, $2}' file.txt`},
		{name: "awk pipe", cmd: `cat file | awk '{print NR, $0}'`},
		{name: "gawk", cmd: `gawk '/pattern/ {print}' file.txt`},
		{name: "awk comparison", cmd: `awk '$3 > 100' file.txt`},
		{name: "awk NR comparison", cmd: `awk 'NR > 5 && NR < 10' file.txt`},
		{name: "awk regex with pipe", cmd: `awk '/foo|bar/ {print}' file.txt`},
		{name: "sort basic", cmd: "sort file.txt"},
		{name: "sort with flags", cmd: "sort -n -r file.txt"},
		{name: "sort unique", cmd: "sort -u file.txt"},
		{name: "find by name", cmd: `find . -name "*.go"`},
		{name: "find with type", cmd: `find /project -type f -name "*.txt"`},
		{name: "find with maxdepth", cmd: `find . -maxdepth 2 -name "*.md"`},
		{name: "find print0", cmd: `find . -name "*.log" -print0`},
		{name: "fd simple", cmd: `fd "*.go"`},
		{name: "fd with type", cmd: `fd -t f "*.txt" src/`},
		{name: "mktemp", cmd: "mktemp"},
		{name: "sleep compound", cmd: "sleep 2 && echo done"},
		{name: "set -e compound", cmd: "set -e && cd dir && npm test"},
		{name: "pushd popd", cmd: "pushd /some/dir && git status && popd"},
		{name: "ps pipe grep", cmd: "ps aux | grep node"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !evalCmd(tt.cmd, testCwd, testCwd) {
				t.Errorf("expected ALLOW for %q", tt.cmd)
			}
		})
	}
}

func TestEvaluate_FallThrough(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
	}{
		{name: "unknown command", cmd: "unknown_cmd arg1 arg2"},
		{name: "bash", cmd: "bash -c 'echo pwned'"},
		{name: "sh", cmd: "sh -c 'echo pwned'"},
		{name: "python", cmd: "python3 script.py"},
		{name: "node", cmd: "node script.js"},
		{name: "cmd subst as command", cmd: `$(echo rm) -rf /`},
		{name: "backtick as command", cmd: "`echo rm` -rf /"},
		{name: "param in command pos", cmd: "$CMD arg"},
		{name: "git -c hooksPath", cmd: "git -c core.hooksPath=/evil commit"},
		{name: "git -c pager", cmd: `git -c core.pager="rm -rf /" log`},
		{name: "git --config-env=", cmd: "git --config-env=CORE_EDITOR=EDITOR commit"},
		// `git config` is excluded from the subcommand allowlist; both read and write forms fall through.
		{name: "git config --get", cmd: "git config --get user.name"},
		{name: "git config --list", cmd: "git config --list"},
		{name: "git config alias bang", cmd: `git config alias.evil "!rm -rf /"`},
		{name: "git config core.hooksPath", cmd: "git config core.hooksPath /tmp/hooks"},
		// `git submodule` mutating subcommands are excluded.
		{name: "git submodule add", cmd: "git submodule add https://github.com/evil/repo"},
		{name: "git submodule update", cmd: "git submodule update --init --recursive"},
		{name: "git submodule foreach", cmd: `git submodule foreach 'echo hi'`},
		// `go run` runs through WriteCheck against the allowed write patterns, so /etc denies but /tmp allows (covered in the
		// Allow tests).
		{name: "go run /etc", cmd: "go run /etc/backdoor.go"},
		{name: "ssh -o ProxyCommand", cmd: `ssh -o "ProxyCommand=evil" test.example.com git status`},
		{name: "ssh -F evil config", cmd: "ssh -F /evil/config test.example.com git status"},
		{name: "ssh -J jump host", cmd: "ssh -J jump.host test.example.com git status"},
		{name: "ssh unknown host", cmd: "ssh evil.host -- git status"},
		{name: "ssh no remote cmd", cmd: "ssh test.example.com"},
		{name: "ssh unsafe inner cmd", cmd: `ssh test.example.com -- "rm -rf /"`},
		{name: "tsh ssh unknown host", cmd: "tsh ssh evil.host git status"},
		{name: "tsh ssh unsafe inner", cmd: `tsh ssh test.example.com "rm -rf /"`},
		{name: "tsh non-ssh subcommand", cmd: "tsh proxy app"},
		{name: "npm exec", cmd: "npm exec -- dangerous-pkg"},
		{name: "pnpm dlx", cmd: "pnpm dlx dangerous-pkg"},
		{name: "yarn dlx", cmd: "yarn dlx dangerous-pkg"},
		{name: "bun x", cmd: "bun x dangerous-pkg"},
		{name: "npx", cmd: "npx some-package"},
		{name: "xargs", cmd: "echo danger | xargs bash"},
		{name: "make", cmd: "make build"},
		{name: "cmake", cmd: "cmake --build ."},
		{name: "docker", cmd: "docker run evil"},
		{name: "kubectl", cmd: "kubectl apply -f evil.yaml"},
		{name: "terraform", cmd: "terraform apply"},
		{name: "command rm", cmd: "command rm -rf /"},
		{name: "command bash", cmd: `command bash -c "echo pwned"`},
		{name: "command python", cmd: "command python3 script.py"},
		{name: "command -- unsafe", cmd: "command -- rm -rf /"},
		// env: simplified to bare-only
		{name: "env with flag", cmd: "env -0"},
		{name: "env with vars", cmd: "env FOO=bar BAZ=qux"},
		{name: "env wrapping safe cmd", cmd: "env FOO=bar git status"},
		{name: "env -i safe cmd", cmd: "env -i FOO=bar git log"},
		{name: "env -u safe cmd", cmd: "env -u HOME git status"},
		{name: "env wrapping bash", cmd: `env bash -c "echo pwned"`},
		{name: "env wrapping python", cmd: "env python3 script.py"},
		{name: "env -S", cmd: `env -S "bash -c echo"`},
		{name: "env --split-string", cmd: `env --split-string="bash -c echo"`},
		// command: simplified to -v/-V only
		{name: "command safe cmd", cmd: "command git status"},
		{name: "command -p safe cmd", cmd: "command -p git log"},
		{name: "command -- safe cmd", cmd: "command -- git status"},
		{name: "bare command", cmd: "command"},
		{name: "awk system", cmd: `awk '{system("echo pwned")}'`},
		{name: "awk getline", cmd: `awk '{cmd | getline result}' file`},
		{name: "awk -f external", cmd: "awk -f script.awk file"},
		{name: "gawk system", cmd: `gawk 'BEGIN{system("echo")}'`},
		{name: "awk file redirect", cmd: `awk '{print > "/tmp/out"}' file`},
		{name: "awk file append", cmd: `awk '{print >> "/tmp/out"}' file`},
		{name: "awk pipe to cmd", cmd: `awk '{print | "sort"}' file`},
		{name: "awk redirect no space", cmd: `awk '{print >"/tmp/out"}' file`},
		{name: "awk redirect abs path", cmd: `awk '{print > /tmp/file}' input`},
		{name: "find -exec", cmd: `find . -exec echo {} \;`},
		{name: "find -execdir", cmd: `find . -execdir echo {} \;`},
		{name: "find -delete", cmd: "find . -name '*.tmp' -delete"},
		{name: "find -ok", cmd: `find . -ok echo {} \;`},
		{name: "fd --exec", cmd: `fd "*.go" --exec echo {}`},
		{name: "fd -x", cmd: `fd "*.go" -x echo {}`},
		{name: "fd --exec-batch", cmd: `fd "*.go" --exec-batch echo`},
		{name: "fd -X", cmd: `fd "*.go" -X echo`},
		{name: "sed -i", cmd: "sed -i 's/foo/bar/' file.txt"},
		{name: "sed -ni", cmd: "sed -ni 's/foo/bar/p' file.txt"},
		{name: "sed --in-place", cmd: "sed --in-place 's/foo/bar/' file.txt"},
		{name: "yq -i", cmd: "yq -i '.foo = 1' file.yaml"},
		// `sort -o` is WRITE_CHECK: it denies when the output path is outside the allowed write dirs (here /etc) and allows
		// when inside (covered in the Allow tests).
		{name: "sort -o outside", cmd: "sort -o /etc/output.txt input.txt"},
		{name: "sed e standalone", cmd: "sed 'e' file"},
		{name: "sed s///e flag", cmd: "sed 's/foo/bar/e' file"},
		{name: "sed s///ge flags", cmd: "sed 's/foo/bar/ge' file"},
		{name: "sed -e with e cmd", cmd: "sed -e 'e' file"},
		{name: "sed -f external", cmd: "sed -f script.sed file"},
		{name: "sed w standalone", cmd: "sed 'w /etc/passwd' file"},
		{name: "sed W standalone", cmd: "sed 'W /etc/passwd' file"},
		{name: "sed s///w flag", cmd: "sed 's/foo/bar/w /etc/passwd' file"},
		{name: "sed s///gw flags", cmd: "sed 's/foo/bar/gw /etc/passwd' file"},
		{name: "sed semicolon w", cmd: "sed 'd;w /etc/passwd' file"},
		{name: "redirect to /etc", cmd: "echo foo > /etc/passwd"},
		{name: "n> redirect outside", cmd: "echo foo 1>/etc/passwd"},
		{name: "redirect with traversal", cmd: "echo foo > ../../etc/passwd"},
		{name: "background", cmd: "echo hello &"},
		{name: "background compound", cmd: "sleep 1 &"},
		{name: "func def", cmd: "foo() { rm -rf /; }"},
		{name: "coproc", cmd: "coproc cat"},
		{name: "trap", cmd: "trap 'echo pwned' EXIT"},
		{name: "eval", cmd: `eval "rm -rf /"`},
		{name: "exec", cmd: "exec rm -rf /"},
		{name: "source", cmd: "source evil_script.sh"},
		{name: "dot source", cmd: ". evil_script.sh"},
		{name: "if unsafe body", cmd: "if true; then rm -rf /; fi"},
		{name: "for unsafe body", cmd: "for f in *; do rm \"$f\"; done"},
		{name: "c-style for unsafe body", cmd: "for ((i=0; i<3; i++)); do rm -rf /; done"},
		{name: "c-style for unsafe expr", cmd: "for ((i=$(rm -rf /); i<3; i++)); do echo $i; done"},
		{name: "while unsafe body", cmd: "while true; do rm -rf /; done"},
		{name: "subshell unsafe", cmd: "(rm -rf /)"},
		{name: "block unsafe", cmd: "{ rm -rf /; }"},
		{name: "case unsafe body", cmd: "case x in *) rm -rf /;; esac"},
		{name: "case unsafe pattern subst", cmd: `case x in $(rm -rf /)) echo match;; esac`},
		{name: "if unsafe cond", cmd: "if rm -rf /; then echo done; fi"},
		{name: "while unsafe cond", cmd: "while rm -rf /; do echo done; done"},
		{name: "arithm unsafe subst", cmd: `(( x = $(rm -rf /) ))`},
		{name: "let unsafe subst", cmd: `let "x=$(rm -rf /)+1"`},
		{name: "time unsafe inner", cmd: "time rm -rf /"},
		{name: "unsafe subst in arg", cmd: `echo "$(rm -rf /)"  `},
		{name: "unsafe proc subst", cmd: "cat <(rm -rf /)"},
		{name: "unsafe param default", cmd: `echo "${HOME:-$(rm -rf /)}" `},
		{name: "pipe to bash", cmd: "echo 'rm -rf /' | bash"},
		{name: "pipe to python", cmd: "echo 'import os' | python3"},
		{name: "multi-line unsafe", cmd: "echo safe\nrm -rf /"},
		{name: "cp target-dir outside", cmd: "cp --target-directory=/etc file.txt"},
		{name: "mv target-dir outside", cmd: "mv --target-directory=/etc file.txt"},
		{name: "cp outside", cmd: "cp file.txt /etc/evil"},
		{name: "mkdir outside", cmd: "mkdir /etc/evil"},
		{name: "rm outside", cmd: "rm -rf /"},
		{name: "touch outside", cmd: "touch /etc/passwd"},
		{name: "safe then unsafe", cmd: "echo hello && rm -rf /"},
		{name: "unsafe then safe", cmd: "rm -rf / && echo hello"},
		{name: "safe pipe unsafe", cmd: "echo data | bash"},
		// --- Tilde expansion bypass (bash expands ~ to $HOME) ---
		{name: "redirect to ~/", cmd: "echo evil >> ~/.bashrc"},
		{name: "touch ~/", cmd: "touch ~/.ssh/authorized_keys"},
		{name: "cp to ~/", cmd: "cp file ~/.profile"},
		{name: "redirect ~/traversal", cmd: "echo evil > ~/../../etc/passwd"},

		// --- Env-var assignments (intentionally unsupported) --- All assignment forms fall through: prefix (`FOO=bar cmd`),
		// bare (`FOO=bar` with no command — dead code without symtab), and DeclClause
		// (`export`/`declare`/`local`/`readonly`).
		{name: "bare assignment", cmd: "FOO=bar"},
		{name: "PATH override prefix", cmd: "PATH=/evil git status"},
		{name: "LD_PRELOAD prefix", cmd: "LD_PRELOAD=/tmp/x.so cat foo"},
		{name: "DYLD_INSERT_LIBRARIES prefix", cmd: "DYLD_INSERT_LIBRARIES=/tmp/x.so cat foo"},
		{name: "GIT_SSH_COMMAND prefix", cmd: "GIT_SSH_COMMAND=/tmp/x.sh git status"},
		{name: "AWS_PROFILE prefix", cmd: "AWS_PROFILE=staging aws sts get-caller-identity"},
		{name: "LANG prefix", cmd: "LANG=C grep -P pattern file"},
		{name: "FOO=bar prefix", cmd: "FOO=bar git status"},
		// DeclClause forms — export, declare, local, readonly — all reject.
		{name: "export FOO", cmd: "export FOO=bar"},
		{name: "export unsafe subst", cmd: "export FOO=$(rm -rf /)"},
		{name: "export compound", cmd: "export FOO=bar && agent-bzl test //pkg:test"},
		{name: "export PATH", cmd: "export PATH=/evil:$PATH"},
		{name: "export LD_PRELOAD", cmd: "export LD_PRELOAD=/tmp/x.so"},
		{name: "declare FOO", cmd: "declare -i num=5"},
		{name: "declare unsafe", cmd: "declare FOO=$(rm -rf /)"},
		{name: "local FOO", cmd: "local foo=bar"},
		{name: "readonly FOO", cmd: "readonly PI=3.14"},

		{name: "redirect with var", cmd: "echo foo > $FILE"},
		{name: "redirect with subst", cmd: "echo foo > $(echo /tmp/file)"},
		{name: "env subst as cmd", cmd: `env $(printf git) status`},
		{name: "env subst safe inner dangerous output", cmd: `env $(printf python3) script.py`},
		{name: "env param as cmd", cmd: `env $CMD arg`},
		{name: "git subst subcommand", cmd: `git $(echo status)`},
		{name: "git param subcommand", cmd: `git $SUBCMD`},
		{name: "npm subst subcommand", cmd: `npm $(echo install)`},
		{name: "cargo subst subcommand", cmd: `cargo $(echo build)`},
		{name: "ssh subst remote cmd", cmd: `ssh test.example.com $(echo 'git status')`},
		{name: "ssh param remote cmd", cmd: `ssh test.example.com $CMD`},
		{name: "awk subst program", cmd: `awk "$(printf '{print}')" file`},
		{name: "awk param program", cmd: `awk "$PROG" file`},
		{name: "sed subst program", cmd: `sed "$(printf 's/a/b/')" file`},
		{name: "sed param program", cmd: `sed "$PROG" file`},
		{name: "find subst flag", cmd: `find . $(echo -exec) echo {} \;`},
		{name: "find param flag", cmd: `find . $FLAG echo {} \;`},
		{name: "fd subst flag", cmd: `fd pattern $(echo --exec) echo`},
		{name: "cp subst path", cmd: `cp file $(echo /tmp/backup)`},
		{name: "mkdir subst path", cmd: `mkdir $(echo /tmp/dir)`},
		{name: "cp param path", cmd: `cp file $DEST`},
		{name: "for body param as cmd", cmd: `for cmd in safe1 safe2; do $cmd; done`},
		{name: "for subst items unsafe body", cmd: `for cmd in $(echo "a b"); do $cmd; done`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if evalCmd(tt.cmd, testCwd, testCwd) {
				t.Errorf("expected FALL-THROUGH for %q", tt.cmd)
			}
		})
	}
}

func TestEvaluate_NoCwd(t *testing.T) {
	t.Run("redirect to dev null", func(t *testing.T) {
		if !evalCmd("git log > /dev/null", "", "") {
			t.Error("expected allow")
		}
	})
	t.Run("redirect to file falls through", func(t *testing.T) {
		if evalCmd("git log > output.txt", "", "") {
			t.Error("expected fall-through")
		}
	})
}

// The walker carries no symbol table: every $VAR / ${VAR} reference is unresolvable and the containing word fails
// wordIsSafe, so any command that depends on a prior assignment falls through to manual prompt.

func TestEvaluate_VarRefFallsThrough(t *testing.T) {
	tests := []struct {
		name string
		cmd  string
	}{
		{name: "redirect with var", cmd: `OUT=/tmp/x; echo y > $OUT`},
		{name: "rm with var", cmd: `TARGET="/tmp/safe" && rm -rf "$TARGET"`},
		{name: "param default", cmd: `rm -rf "${DIR:-/etc}"`},
		{name: "param substitution op", cmd: `rm -rf "${DIR:+/etc}"`},
		{name: "indirect var", cmd: `rm -rf "${!REF}"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if evalCmd(tt.cmd, testCwd, testCwd) {
				t.Errorf("expected FALL-THROUGH for %q", tt.cmd)
			}
		})
	}
}

func TestEvaluate_WriteThenExecute(t *testing.T) {
	t.Run("fall_through", func(t *testing.T) {
		tests := []struct {
			name string
			cmd  string
		}{
			{
				name: "redirect then execute same path",
				cmd:  `echo '#!/bin/bash' > /tmp/evil.sh && /tmp/evil.sh`,
			},
			{
				name: "redirect then execute relative",
				cmd:  "echo '#!/bin/bash' > script.sh && ./script.sh",
			},
			{
				name: "append redirect then execute",
				cmd:  `echo '#!/bin/bash' >> /tmp/evil.sh && /tmp/evil.sh`,
			},
			// `F=... && echo > "$F" && /tmp/evil.sh` falls through because $F is unresolvable now (no symtab) — the redirect
			// target fails wordIsSafe before the write is even attempted.
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if evalCmd(tt.cmd, testCwd, testCwd) {
					t.Errorf("expected FALL-THROUGH for %q", tt.cmd)
				}
			})
		}
	})

	t.Run("allow", func(t *testing.T) {
		tests := []struct {
			name string
			cmd  string
		}{
			{
				name: "redirect without subsequent execute",
				cmd:  `echo data > /tmp/output.txt && echo done`,
			},
			{
				name: "redirect in subshell does not leak",
				cmd:  `(echo data > /tmp/evil.sh) && echo done`,
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if !evalCmd(tt.cmd, testCwd, testCwd) {
					t.Errorf("expected ALLOW for %q", tt.cmd)
				}
			})
		}
	})
}

func TestEvaluate_Empty(t *testing.T) {
	if evalCmd("", testCwd, testCwd) {
		t.Error("empty command should fall through")
	}
	if evalCmd("   ", testCwd, testCwd) {
		t.Error("whitespace command should fall through")
	}
}

// TestEvaluate_MatchedNames pins the matched-names list surfaced to the static_bash_rules decider. Order follows source
// order, duplicates are kept (a compound's two `git` pieces both record), and a fall-through clears the list to empty.
func TestEvaluate_MatchedNames(t *testing.T) {
	tests := []struct {
		name        string
		cmd         string
		wantAllowed bool
		wantMatched []string
	}{
		{
			name:        "single command",
			cmd:         "git status",
			wantAllowed: true,
			wantMatched: []string{"git"},
		},
		{
			name:        "compound &&",
			cmd:         "ls && git status",
			wantAllowed: true,
			wantMatched: []string{"ls", "git"},
		},
		{
			name:        "duplicates kept",
			cmd:         "git status && git diff",
			wantAllowed: true,
			wantMatched: []string{"git", "git"},
		},
		{
			name:        "pipe",
			cmd:         "cat file.txt | grep pattern",
			wantAllowed: true,
			wantMatched: []string{"cat", "grep"},
		},
		{
			name:        "fall through clears matched",
			cmd:         "definitely_not_a_real_command --frobnicate",
			wantAllowed: false,
			wantMatched: nil,
		},
		{
			name:        "partial match still falls through",
			cmd:         "git status && definitely_not_a_real_command",
			wantAllowed: false,
			wantMatched: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := Evaluate(Input{
				Command:  tt.cmd,
				Cwd:      testCwd,
				RuleSet:  testRuleSet,
				Checkers: testCheckers,
				Config:   testCfg,
			})
			if v.Allowed != tt.wantAllowed {
				t.Errorf("allowed = %v, want %v", v.Allowed, tt.wantAllowed)
			}
			if !slicesEqual(v.Matched, tt.wantMatched) {
				t.Errorf("matched = %v, want %v", v.Matched, tt.wantMatched)
			}
		})
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
