# Static Bash Rules Design

This document describes the design of the static Bash rules subsystem — one of two decisioning subsystems in the
claude-auto-permission hook. For the orchestrator that combines this subsystem's verdict with peer subsystems', see
[`design.md`](design.md). For the LLM classifier, see [`llm-classifier-design.md`](llm-classifier-design.md).

## Context

### Motivation

Claude Code's native permission model for `Bash` calls is roughly `Bash(<command> <subcommand>:*)` — an exact-prefix
match against a flat command line. That works for `git status`, but anything with structure beyond a flat invocation
falls back to a prompt:

- `for f in *.go; do go test "$f"; done`
- `git diff --stat $(git merge-base HEAD main)`
- `if go build ./...; then go test ./...; fi`
- `result=$(cat VERSION); echo "$result"`

Each is benign. None can be expressed as a flat allowlist pattern. Each is a permission prompt under the native model.

The result is permission fatigue: the user approves dozens of obviously-safe commands per session. Two common
reactions, both bad:

- Run with `--dangerously-skip-permissions` and trust the agent on everything.
- Click **Allow** instinctively without reading the prompt.

This subsystem closes the gap by parsing the actual Bash AST and walking it node by node, checking every component
against a config-driven rule set. When every component proves safe, it auto-approves. When it can't prove safety, it
stays silent and falls through to Claude Code's normal flow — same UX as if this layer weren't there.

### Goals & Non-Goals

**Goals:**

- **Parse, don't pattern-match.** Use a proper Bash parser. Walk the AST. Inspect every node — every statement in a
  compound chain, each side of a pipe, the condition and body of `if`/`for`/`while`/`case` blocks, subshells, command
  substitutions, redirects.
- **Sound by construction.** When a node can't be proven safe, fall through. The walker never approves a node it
  doesn't understand.
- **Microsecond-cheap.** No I/O, no LLM, no network. Pure CPU work — fast enough to run on every Bash call without
  noticeable latency.
- **User-extensible.** Bundled defaults cover ~80 common dev tools. Users can replace or augment with custom rules
  expressed in a structured DSL.
- **Composable with peer deciders.** Verdict is a vote, not a final answer. The LLM classifier (when enabled) sees
  the same call and can veto.

**Non-goals:**

- **Security boundary.** Documented in the README — this isn't a sandbox. A malicious dependency, a build script, or a
  sufficiently confused agent can still do harm through commands the walker considers safe (`npm install`, `cargo
  build`, `go test`). The walker reduces friction; it doesn't eliminate risk.
- **Cross-call analysis.** Each Bash call is evaluated in isolation. Multi-step attacks that split steps across
  separate `Bash(...)` invocations bypass the per-command analysis. The classifier (when enabled) sees both calls in
  the transcript and can catch the chain.
- **Variable substitution.** No symbol table. Any `$VAR` / `${VAR}` reference is unresolvable, and the containing word
  fails the safety check. The trade-off is intentional — see [Limitations](#limitations).
- **Block / deny verdicts.** The current capability is `allow` or `silent`. Deny is reserved for future rules; the
  walker doesn't emit it today.

## Architecture

The static Bash subsystem is a **decider plugin** in the broader hook orchestrator. It votes one of two values on each
tool call:

- **`allow`** — the tool is `Bash`, the command parsed, and every node in the AST cleared the configured rule set.
  The reason field carries the matched rule names so the decision log attributes the verdict.
- **`silent`** — anything else: not a `Bash` call, malformed input, parse error, empty command, or any node that
  failed a check. No opinion; Claude Code's normal flow handles the call.

```
                ┌──────────────────────────────────────────────────┐
                │ Static Bash decider                              │
                │                                                  │
                │  tool input ──▶ JSON unmarshal ──▶ Bash parser   │
                │                                       │          │
                │                                       ▼          │
                │                              AST walker          │
                │                            (recursive)           │
                │                                       │          │
                │                                       ▼          │
                │                       per-node rule lookup       │
                │                              + checks            │
                │                                       │          │
                │                                       ▼          │
                │                       allow (matched rules)      │
                │                            or silent             │
                └──────────────────────────────────────────────────┘
```

The decider is pure CPU work: it never reads the transcript, never makes a network call, never even reads the
filesystem (path checks are pattern-only). That's why it can run on every Bash call without latency cost.

## AST Walker

The walker uses [`mvdan/sh`](https://github.com/mvdan/sh) — a robust Bash parser — and recurses through the parse
tree. Every node type that appears in real-world Bash gets a structural check.

### Node Types Handled

| Node                                    | Treatment                                                                                                                 |
|-----------------------------------------|---------------------------------------------------------------------------------------------------------------------------|
| **CallExpr**                            | The base case: a single command invocation. Looked up in the rule set; checks evaluated against parsed flags/positionals. |
| **BinaryCmd** (`&&`, `\|\|`, `\|`, `;`) | Both sides recursively checked. All sides must clear.                                                                     |
| **Subshell** (`( ... )`)                | Statements recursively checked.                                                                                           |
| **Block** (`{ ... }`)                   | Statements recursively checked.                                                                                           |
| **IfClause**                            | Condition, then-branch, and else-branch all recursively checked.                                                          |
| **WhileClause**                         | Condition and body recursively checked.                                                                                   |
| **ForClause**                           | Loop variables/items checked as words; body recursively checked.                                                          |
| **CaseClause**                          | Subject word checked; each case pattern and body recursively checked.                                                     |
| **TestClause** (`[[ ... ]]`)            | Test expression checked structurally.                                                                                     |
| **ArithmCmd** (`(( ... ))`)             | Arithmetic expression checked structurally.                                                                               |
| **TimeClause** (`time ...`)             | Wrapped statement recursively checked.                                                                                    |
| **LetClause**                           | Each arithmetic expression checked structurally.                                                                          |
| **Redirects**                           | Each redirect's target word checked; output redirects are write-checked against allowed paths.                            |
| **FuncDecl**                            | **Always rejected** — defines a function in the current shell, opens an arbitrary code path.                              |
| **CoprocClause**                        | **Always rejected** — coprocess execution.                                                                                |

A few invariants enforced everywhere:

- **Background statements** (`cmd &`) are rejected — fire-and-forget execution is hard to reason about.
- **Coprocesses** are rejected for the same reason.
- **Shell keywords with arbitrary code semantics** — `eval`, `exec`, `source`, `.`, `trap` — are rejected
  unconditionally.
- **Env-var assignments in any form** — prefix (`FOO=bar cmd`), bare (`FOO=bar`), or `export`/`declare`/`local`/
  `readonly` — fall through. See [Limitations](#limitations) for why.

### Word-Level Safety

Each word (the unit of an argument or filename in Bash) is checked structurally:

- **Literal parts** are kept as-is.
- **Single-quoted parts** are literal — kept as-is.
- **Double-quoted parts** that contain only literals are kept; quoted parameter expansions like `"$VAR"` fail because
  the walker has no symbol table to resolve them.
- **Unquoted parameter expansions** (`$VAR`, `${VAR}`) fail — output would be re-interpreted by the shell.
- **Command substitutions** (`$(...)`, `` `...` ``) recursively re-enter the walker against the inner command, with
  the wrapping context tracked: in positions where the output would be interpreted as code, the substitution fails
  even if its inner command is safe.
- **Process substitutions** (`<(...)`, `>(...)`) are not currently supported and fail.
- **Globs** (`*.go`) are kept as literals — the shell expands them at execution; we don't pre-expand.

### Write-Then-Execute Defense

A common attack pattern is *write a file, then execute it*: `echo evil > /tmp/x.sh && bash /tmp/x.sh`, or write a
malicious binary to a directory on `$PATH` and then invoke it by name. The walker tracks paths written to during the
current command and rejects any later command that resolves to one of those paths.

Limitation: the resolution happens against `cwd`, not against `$PATH`. So writing `/usr/local/bin/cat` and then calling
`cat` (relying on PATH lookup) is **not caught by this check**. The mitigation is configuration: don't add `$PATH`
directories to your `allow_write_patterns`.

### Recursion Across Boundaries

A few rule types re-enter the walker against inner commands:

- **`ssh host -- <inner>`** / **`tsh ssh host -- <inner>`** — the inner command is recursively walked, but with the
  *remote host's* allowed write paths instead of the local ones. Local `allow_project_write` does not apply across the
  host boundary.
- **`go run` / similar wrappers** that conceptually take a sub-command can use a recursive evaluation rule type to
  walk the wrapped command.
- **Command substitutions** — the inner command is walked under the same context as the outer call.

## Rule DSL

The rule set is structured data, not regexes. The grammar is defined in protobuf; users write rules in protobuf
text-format. The shape:

```
RuleSet
  └─ commands[]
       └─ CommandSpec
            ├─ names: ["git", "git2"]              (aliases share a spec)
            └─ checker: one of
                 ├─ CustomRules                    (the DSL)
                 ├─ SedChecker                     (built-in: sed has its own checker)
                 └─ AwkChecker                     (built-in: awk too)
```

A `CustomRules` is an ordered list of `Rule`s. For a command to be allowed:

- **All `Rule`s must pass.** A failed rule rejects the command immediately.
- **At least one `Allow` must contribute a positive vote.** A `CustomRules` with only `Deny` rules can never approve;
  it can only act as a gate that other rules don't trip.

A `Rule` is one of:

- **`Allow { condition }`** — passes when the condition is true.
- **`Deny { condition }`** — passes when the condition is false (deny not triggered). When triggered, the command
  is rejected.
- **`AlwaysAllow {}`** — convenience for unconditional allow.
- **`AlwaysDeny {}`** — convenience for unconditional deny.

### Conditions

Conditions are the building blocks. The most useful:

| Condition                             | Semantics                                                                                          |
|---------------------------------------|----------------------------------------------------------------------------------------------------|
| **`always {}`**                       | Always true. Used in `Allow` to mark a command as safe with any args.                              |
| **`not { ... }`**                     | Inverts a nested condition.                                                                        |
| **`no_args {}`**                      | True when the command has no arguments.                                                            |
| **`has_double_dash {}`**              | True when the command has a `--` separator.                                                        |
| **`has_flag_matching { ... }`**       | True when at least one flag matches. Match shapes: exact set, or short-char + long-prefix pattern. |
| **`flag_arg_check { ... }`**          | True when the flag is absent **or** its argument passes a `Check` (e.g. write-allowed path).       |
| **`every_flag_matches { ... }`**      | True when *every* flag in the command is in the allowed set. Unlisted flag → fail.                 |
| **`every_positional_passes { ... }`** | True when every positional argument passes a `Check`.                                              |
| **`subcommands { ... }`**             | First positional must match a known subcommand; that subcommand has its own rules.                 |
| **`max_positionals { count }`**       | True when at most N positionals are present.                                                       |

A `Check` (used by `flag_arg_check` and `every_positional_passes`) is one of:

- **`write_check {}`** — target path must resolve under one of the configured allowed-write directories.
- **`ssh_remote_eval {}`** — target is a hostname; recursively walk the rest of the args as the remote command, under
  that host's write scope.
- **`recurse_eval {}`** — recursively walk the joined remaining args as a nested command (for wrappers like `go run`).

### Subcommand Rules

Subcommand rules are how the DSL handles tools like `git` or `npm` whose risk profile depends entirely on the
subcommand:

```
git status        → safe with any args
git push          → needs careful checks (no -c, no --upload-pack, etc.)
git filter-branch → fall through (history-rewriting, dangerous)
git foo           → unknown subcommand, fall through
```

A `subcommands` condition has two slots:

- **`allowed_with_any_args`** — subcommands that are safe with any arguments (`status`, `log`, `diff`, …).
- **`with_rules`** — entries that bind a subcommand (or several) to a nested `CustomRules` block, *or* reference
  another top-level command spec by name (so e.g. `git push` could share a rule spec with `git push-other-thing`).

### Bundled Defaults

The default rule set ships compiled-in and is enabled per-project with `use_default_rules {}`. It covers ~80 common
development tools across categories:

- **Read-only utilities** — `cat`, `ls`, `grep`, `jq`, `find`, `wc`, `diff`, ...
- **Shell builtins safe with any args** — `cd`, `echo`, `test`, `pwd`, `printenv`, ...
- **Source control** — `git`, with subcommand allowlists tuned for the safe ones (`status`, `log`, `diff`, ...) and
  dangerous-flag denylists for the ambiguous ones (`-c`, which can set arbitrary config).
- **Package managers** — `npm`, `yarn`, `pnpm`, `pip`, `go`, `cargo`, ... with subcommand allowlists.
- **File processing** — `sed` and `awk` get their own dedicated checkers (rejecting code-execution flags like `-e`,
  `-i`, and `system()` / `getline`).
- **Network** — `curl`, `wget` for trusted URL patterns; `ssh`/`tsh` for trusted hosts.
- **macOS / Linux specifics** — `screencapture`, `mdfind`, `pbcopy`, etc.

The default rules are the best example corpus for the DSL — users writing custom rules can read them as a reference.

## Configuration

All configuration is per-project (matched by glob patterns against the cwd / project root):

- **`use_default_rules {}`** — opt into the bundled defaults. Mutually exclusive with `custom_command_rules`.
- **`custom_command_rules { rule_set { ... } }`** — replace the defaults entirely with user-defined rules.
- **`allow_write_patterns: "..."`** — glob patterns marking writable paths. Used by `write_check` conditions.
- **`allow_project_write: true`** — shorthand for "writes anywhere under `$CLAUDE_PROJECT_DIR` are allowed."
- **`remote_hosts { ... }`** — per-host config for SSH/tsh recursion: `host_patterns` and `allow_write_patterns`
  scoped to that host.

When multiple projects' patterns match (e.g. a `/**` global plus a project-specific entry), their rule sets and
write patterns are unioned. Unions are conservative: anything one matching project allows, the merged set allows.

### Custom Rule Example

A trivial rule for a hypothetical CLI:

```txtpb
custom_command_rules {
  rule_set {
    commands {
      names: "mytool"
      custom_rules {
        rules {
          allow {
            condition {
              subcommands {
                allowed_with_any_args: ["status", "version", "list"]

                with_rules {
                  names: ["delete", "destroy"]
                  custom_rules {
                    # Require --dry-run for destructive subcommands.
                    rules {
                      allow {
                        condition {
                          has_flag_matching {
                            exact { flags: "--dry-run" }
                          }
                        }
                      }
                    }
                  }
                }
              }
            }
          }
        }
      }
    }
  }
}
```

`mytool status`, `mytool version`, `mytool list` allowed with any further args. `mytool delete --dry-run` allowed.
`mytool delete` (without `--dry-run`) and `mytool foo` fall through.

## Verdict Composition With Peer Deciders

This subsystem casts one vote among potentially several. The orchestrator combines votes with deny-wins precedence
(see [`design.md`](design.md) for the full combiner). Two interactions worth noting:

- **A static `allow` is not the final word.** The classifier (when enabled) still runs and can veto if it sees a
  user boundary in the transcript or detects something the structural walker can't reason about. This is by design —
  the classifier is what catches semantic boundaries the walker can't express.
- **A static `silent` is no opinion.** Falls through to whatever else votes. With the classifier disabled, a silent
  static verdict means Claude Code's native permission flow handles the call (the user approves manually). With the
  classifier enabled, the classifier judges.

The walker never emits `deny` today. The capability is reserved for a future use case (e.g. a configured "always
deny" pattern), but the philosophy of "only auto-approve, never block" leaves Claude Code's normal flow — and the
classifier — as the only path that ever blocks.

## Limitations

This is a best-effort convenience layer, not a security boundary. It aims to auto-approve the vast majority of safe
commands the agent generates while catching the obvious dangerous ones. It does not protect against:

- **Build-tool code execution.** `cargo build`, `npm install`, `go test` are allowed because they're standard
  development operations, but they execute build scripts, lifecycle hooks, and test code that could do anything. A
  malicious dependency or a sufficiently confused agent could exploit this.
- **Multi-step attacks across separate Bash calls.** Each call is evaluated in isolation. An attack that splits its
  steps — write a malicious script in one call, execute it in the next — bypasses per-command analysis. The
  classifier (when enabled) sees both calls in the transcript and can catch the chain.
- **Interpreter / compiler arguments.** Build tools and compilers (`rustc`, `tsc`, `eslint`) process project files
  that could theoretically contain malicious code triggered by compilation.

A few intentional non-features that follow:

- **No env-var assignment support.** All forms — prefix (`FOO=bar cmd`), bare (`FOO=bar`), and `export` /`declare` /
  `local` /`readonly` — fall through to manual prompt. The alternative is allowlisting value-only var names, which
  would have to be kept in sync with every interpreter's evolving exec hooks (`LD_PRELOAD`, `BASH_ENV`,
  `GIT_SSH_COMMAND`, `NODE_OPTIONS`, ...). For stable per-project preferences, set the var in your shell rc once.
- **No variable substitution.** No symbol table; any `$VAR` / `${VAR}` reference is unresolvable and the containing
  word fails. `OUT=/tmp/x; cp src "$OUT"` falls through.
- **Don't add `$PATH` directories to `allow_write_patterns`.** A write to a `$PATH` directory by the same name as an
  existing allowlisted command (`echo evil > /usr/local/bin/cat && cat`) bypasses the write-then-execute check,
  because relative-path resolution happens against `cwd`, not against `$PATH`. This is configuration advice, not a
  walker flaw.

The walker reduces friction for the common case while keeping Claude Code's manual approval as the backstop for
anything unusual. When the classifier is enabled, it adds a transcript-aware semantic veto on top.
