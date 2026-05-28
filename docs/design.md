# Design

This document describes the overall architecture of claude-auto-permission — the hook lifecycle, the orchestrator and
decider model, decision combination, and cross-cutting concerns (config loading, caching, logging, concurrency).

For subsystem-specific designs, see:

- [`static-bash-rules-design.md`](static-bash-rules-design.md) — the Bash AST walker and rule DSL.
- [`llm-classifier-design.md`](llm-classifier-design.md) — the optional LLM-based classifier subsystem.

For the package-level code map, see [`code-architecture.md`](code-architecture.md).

## PreToolUse Hook

Claude Code lets external tools register hooks at well-defined lifecycle events. claude-auto-permission registers
exactly one: **PreToolUse**, which fires before every tool call. The hook reads a single JSON event from stdin, makes
a decision, and writes the decision (or nothing) to stdout. Claude Code consumes the stdout decision and decides
whether to execute the call, prompt the user, or refuse.

The official Claude Code hook-resolution diagram lays out how a hook decision interacts with the rest of the
permission machinery:

![Claude Code hook resolution flow](https://mintcdn.com/claude-code/-tYw1BD_DEqfyyOZ/images/hook-resolution.svg?w=2500&fit=max&auto=format&n=-tYw1BD_DEqfyyOZ&q=85&s=a91d1baecae1e39c6292a174b8242898)

The crucial property — established by the Claude Code docs and verified in the harness source — is that a PreToolUse
hook decision **never overrides** user-configured `permissions.deny` or `permissions.ask` rules:

- A matching `permissions.deny` rule blocks the call regardless of what the hook returned.
- A matching `permissions.ask` rule still prompts the user, even if the hook returned "allow".
- Hard-coded safety paths (`.git/`, `.claude/`, shell rcs) remain bypass-immune.
- A hook **allow** only short-circuits the *interactive prompt* for calls that would otherwise ask.

So a single PreToolUse hook can both auto-approve known-safe calls and veto dangerous ones, without compromising the
user's own deny rules. Deny-first precedence holds. That's why one hook is enough.

## Hook Lifecycle

Per invocation, the hook does the following:

1. **Read** one `PreToolUse` event from stdin (tool name, tool input, cwd, transcript path, session id, permission
   mode, etc.).
2. **Resolve** Claude Code's per-call environment: project root, the union of reachable settings.json files
   (managed → user → project → local), the resolved CLAUDE.md tree (with `@-imports` inlined). All of this is bundled
   into one immutable `Env` value passed to every decider.
3. **Run each registered decider** against the same input + `Env`. Deciders vote independently and don't see each
   other's verdicts.
4. **Combine** the votes via the precedence rules (see below).
5. **Write** the wire output to stdout: `{"hookSpecificOutput": {"permissionDecision": "allow" | "deny" | "ask"}}`,
   or nothing on `silent` / `passthrough`.
6. **Append** a JSONL line to the decision log with the combined verdict and a per-decider breakdown.

The hook is single-shot. No daemon, no IPC, no shared state across invocations beyond what the filesystem holds (the
auto-mode policy cache, per-session backstop counters, the decision log).

## Orchestrator & Decider Model

The architecture's core is a small interface — the **decider** — and a combiner that reduces a list of votes to a
final verdict.

A decider is a plugin that votes on each tool call. Today there are two:

- **Static Bash decider** — see [`static-bash-rules-design.md`](static-bash-rules-design.md). Microsecond-cheap AST
  walker; votes `allow` or `silent`.
- **LLM classifier decider** — see [`llm-classifier-design.md`](llm-classifier-design.md). Transcript-aware semantic
  judge; votes any of the five values below.

Adding a third decider (external policy server, sandbox-status checker, …) is a one-file change: implement the
interface, register it. The combiner is content-free about how many deciders there are.

### Five-Valued Vote

Each decider returns one of five votes:

| Vote          | Meaning                                                                                                                                                                                                                  | Wire output                                         |
|---------------|--------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------|-----------------------------------------------------|
| `allow`       | This decider thinks the call should fire.                                                                                                                                                                                | `{"permissionDecision": "allow"}`                   |
| `deny`        | This decider thinks the call should be blocked. Reason is required and surfaces to the user.                                                                                                                             | `{"permissionDecision": "deny", "reason": ...}`     |
| `ask`         | This decider thinks the user should be prompted regardless of any other vote.                                                                                                                                            | `{"permissionDecision": "ask"}`                     |
| `passthrough` | This decider had a stake in the call but couldn't vote (provider outage, transcript read failure, prompt build failure, ...). Beats `allow` so a peer's permissive vote can't fast-path while this one is incapacitated. | (none — Claude Code's normal flow handles the call) |
| `silent`      | This decider has no opinion (not its tool, skip-list hit, disabled per project, ...).                                                                                                                                    | (none — Claude Code's normal flow handles the call) |

The split between `silent` and `passthrough` is load-bearing under the combiner. They produce identical wire output,
but they mean different things:

- `silent` = "this isn't my problem" — a non-Bash tool reaching the static Bash decider, a skip-listed tool reaching
  the classifier, a disabled classifier.
- `passthrough` = "this *was* my problem and I should have weighed in, but I can't right now" — the Bedrock provider
  is down, the transcript read failed, a sanitizer panicked.

The combiner uses the distinction to keep peer deciders composable under partial failure: a permissive `allow` from
one decider should not stand when a peer that *should* have opined cannot.

### Combination Rules

The orchestrator combines votes with the following precedence:

```
deny > ask > passthrough > allow > silent
```

The full combiner contract:

- **`deny` short-circuits.** Once any decider denies, no later decider can override. The orchestrator skips remaining
  deciders (logging them as "skipped: prior decider denied" so the breakdown is complete) and emits the deny.
- **`ask`, `passthrough`, `allow`, `silent` never short-circuit.** A later decider must always be able to veto an
  earlier permissive vote. This is the load-bearing invariant of the architecture — the headline scenario "static
  layer says allow, classifier sees a user boundary and says deny" relies on it.
- **An empty result list collapses to `silent`.** No deciders means no opinion.

A worked example showing why the precedence matters:

| Static decider | Classifier    | Combined verdict | What happens                                                  |
|----------------|---------------|------------------|---------------------------------------------------------------|
| `allow`        | (any)         | runs both        | static doesn't short-circuit; classifier always gets to vote  |
| `allow`        | `allow`       | `allow`          | both happy; auto-approve                                      |
| `allow`        | `deny`        | **`deny`**       | the headline scenario — classifier vetoes a static allow      |
| `allow`        | `ask`         | `ask`            | backstop tripped; force user prompt despite static allow      |
| `allow`        | `passthrough` | `passthrough`    | classifier withheld; emit nothing, Claude Code's flow handles |
| `allow`        | `silent`      | `allow`          | classifier abstained; static's allow stands                   |
| `silent`       | `allow`       | `allow`          | static abstained; classifier's allow stands                   |
| `silent`       | `deny`        | `deny`           | classifier denies                                             |
| `silent`       | `silent`      | `silent`         | nobody had an opinion; emit nothing                           |
| `deny`         | (any)         | `deny`           | static denies; short-circuit                                  |

`passthrough` and `silent` produce identical wire output. The split exists for the combiner.

## Per-Call Environment

Several pieces of "what does Claude Code look like to us right now" need to be resolved once per hook invocation and
shared with every decider, rather than re-resolved per decider:

- **cwd** — the working directory Claude Code was launched from.
- **project root** — `$CLAUDE_PROJECT_DIR` when set, otherwise cwd. Stable across worktree moves.
- **permission scope** — the union of `permissions.additionalDirectories` and `permissions.deny` patterns from every
  reachable `settings.json` (managed system + user + project + local). Plus the full settings-path candidate list,
  for downstream cache fingerprinting.
- **resolved CLAUDE.md** — the walked CLAUDE.md tree with `@-imports` recursively inlined.

Each piece can fail independently (a missing `~/.claude/settings.json` is fine on a fresh machine; a malformed one is
logged but doesn't block the call). Failures degrade to zero values rather than blocking the user. Deciders that
don't need a particular resolution can simply ignore it — the static Bash decider, for example, only uses cwd and
project root.

## Configuration

User configuration lives in a single text-format protobuf file at `~/.config/claude-auto-permission/config.txtpb`,
overridable via `CLAUDE_AUTO_PERMISSION_CONFIG`. The schema is in [`proto/config/v1/config.proto`](../proto/config/v1/config.proto).

### Per-Project Matching

The config is structured as a list of **projects**, each scoped by glob-style `path_patterns` matched against the
hook's working directory. Multiple projects can match the same cwd (e.g. a `/**` global default plus a project-specific
override), in which case:

- For **rule sets** (static Bash) and **write patterns**: the matching projects' contributions are **unioned** — any
  rule any matching project allows, the merged set allows.
- For **classifier config**: the most-specific match wins (longest matching pattern). Classifier behavior is one set
  of knobs per project; merging would be ambiguous.

Projects without a `static_bash_rules` block contribute nothing to the static layer. Projects without an
`llm_classifier` block leave the classifier disabled for matching cwds.

### Runtime Knobs

Process-level settings (cache directory, decision log size, `claude` config dir override, debug dump opt-in) live in
a top-level `runtime { ... }` block. Most users don't touch these.

## Caching

Several pieces of state are cached on disk under `$CACHE_DIR`
(default `~/.cache/claude-auto-permission/`, override via `CLAUDE_AUTO_PERMISSION_RUNTIME_CACHE_DIR` or config).

| Cache                             | Purpose                                                                               | Invalidation                                                                                                                                      |
|-----------------------------------|---------------------------------------------------------------------------------------|---------------------------------------------------------------------------------------------------------------------------------------------------|
| **Auto-mode policy cache**        | Memoizes the result of shelling out to `claude auto-mode config` (a 250–500 ms call). | Key folds in `claude` binary realpath / size / mtime, plus path / existence / size / mtime of every reachable `settings.json`. 24 h TTL backstop. |
| **Per-session backstop counters** | Tracks consecutive and total classifier blocks for the runaway-block circuit breaker. | Implicitly per-session-id; counters reset on classifier allow, fully zeroed when the backstop fires its one-shot ask.                             |
| **Decision log**                  | Audit trail of every hook invocation's verdict and per-decider breakdown.             | Tail-truncates at a configurable size cap (default 50 MiB); rotates `<path>` → `<path>.1`.                                                        |
| **Classifier dumps** *(opt-in)*   | Full provider request/response dumps for debugging.                                   | Manual cleanup. Off by default.                                                                                                                   |

The auto-mode policy cache is the most interesting one. It exists because the shell-out to `claude auto-mode config`
is far too expensive to run on every hook invocation, but the inputs to it can change at any moment — the user might
edit `~/.claude/settings.json` between two tool calls. The fingerprint covers everything that could affect the result:
the binary itself (catches Claude Code upgrades) and every settings file Claude Code itself would read.

## Logging

The decision log is one JSONL file under `$CACHE_DIR/decisions.log.jsonl`. Each line is one hook invocation. The
schema:

- **Top-level fields**: timestamp, session id, agent id (subagents), tool name, cwd, project root, permission mode,
  SHA fingerprint of the tool input, the orchestrator's combined `decision`, and `reason`.
- **Per-decider breakdown** under `deciders`: each decider's vote, reason, latency in ms, and optional metadata
  (e.g. the classifier records its `provider` and `model` here).

A characteristic line looks roughly like:

```jsonl
{"ts":"...","session_id":"...","tool":"Bash","cwd":"/proj","input_sha":"...",
 "decision":"deny","reason":"User explicitly forbade git push earlier in session",
 "deciders":{
   "static_bash_rules":{"decision":"allow","reason":"matched static rule: git","latency_ms":1},
   "llm_classifier":{"decision":"deny","reason":"User explicitly forbade...","latency_ms":1843,
                     "meta":{"provider":"bedrock","model":"us.anthropic.claude-haiku-4-5-20251001-v1:0"}}}}
```

This shows the headline scenario the architecture exists for: the static engine voted `allow`, the classifier saw a
user boundary in the transcript and vetoed `deny`, and the combiner's deny-wins precedence emitted `deny`.

For silent and passthrough verdicts, the per-decider `reason` is the highest-value debug signal — it explains *why*
the decider stayed quiet (skip-list? backstop? provider error? not the right tool?). Always populated.

The log is enabled when **any** project has `log_decisions: true`. Live tail with `tail -F | jq` is the
recommended workflow for understanding what the hook is doing in real time.

## Concurrency Model

claude-auto-permission has two pieces of cross-process shared state. Each uses the pattern that fits its access
profile.

### Decision log appends — `O_APPEND` atomic, flock'd rotation

Decision log writes are append-only and small (well under PIPE_BUF, 4 KiB on Linux/macOS), so `O_APPEND` is
POSIX-atomic and concurrent appends don't need locks. The parent Claude Code process plus any subagent processes can
all append concurrently without coordination.

Rotation (when the active file exceeds the size cap) does need synchronization to prevent two processes from both
renaming. The stat+rename pair is guarded by a non-blocking exclusive flock on the active file: losers skip the
rotation and let the winner do it. Verified under both in-process goroutine stress and 5-process cross-process tests.

### Per-session backstop counters — flock'd read-modify-write

Backstop counters mutate on every classifier block. The parent Claude Code process plus N subagent processes can bump
the same counter concurrently — losing an increment would skew the runaway-block detection.

Each session id maps to one file. Every increment opens with `O_RDWR`, takes an exclusive POSIX advisory file lock,
reads, mutates, writes, unlocks, closes. Verified under both 100-goroutine in-process stress and 5-process
cross-process tests.

## Failure Posture

A few principles run through the entire system:

- **Never block the user blind.** Any failure path that the hook can't recover from emits no wire output, letting
  Claude Code's normal permission flow handle the call. The user is never left staring at a frozen Claude.
- **Withhold, don't fail open.** When a decider was supposed to weigh in but couldn't, it returns `passthrough`
  instead of `silent` — overriding peer permissive votes so the user doesn't accidentally get auto-approval from
  a partial system.
- **Log everything.** Every invocation produces a decision log line, even if no wire output is emitted, so the user
  can reconstruct what happened after the fact.
- **Cache what's expensive, fingerprint everything that could change.** The auto-mode policy cache key is paranoid by
  design — better to occasionally re-shell-out than to silently serve a stale policy.

The walker is sound: it never auto-approves a node it doesn't understand. The classifier's threat model rests on
transcript stripping (see [`llm-classifier-design.md`](llm-classifier-design.md)). The orchestrator's combiner is a
total function on the five votes, with no path to "no decision was emitted" except `silent` / `passthrough` — both of
which mean Claude Code's normal flow runs.

The system as a whole inherits Claude Code's deny-first precedence on top of all of this: even a confident hook
`allow` doesn't bypass user-configured `permissions.deny`, so the worst case at the system level is "auto-approval of
calls the user didn't realize were ambiguous" — not silent execution of denied operations.
