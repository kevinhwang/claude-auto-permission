# Code Architecture

A high-level map of the `claude-auto-permission` codebase: the conceptual layers, what each layer owns, and where they
touch.

For the *why* behind these shapes — threat models, prompt anatomy, decision-combination semantics, worked examples —
see the design docs:

- [`design.md`](design.md) — overall hook architecture, decision pipeline, caching, concurrency, logging.
- [`static-bash-rules-design.md`](static-bash-rules-design.md) — static Bash AST walker and rule DSL.
- [`llm-classifier-design.md`](llm-classifier-design.md) — LLM classifier subsystem.

This doc sticks to the structural picture.

## At A Glance

```
┌────────────────────────────────────────────────────────────┐
│ Hook entrypoint                                            │
│   cmd/claude-auto-permission · internal/app                │
│   (read HookInput → dispatch → write wire output → log)    │
└──────────────────────────┬─────────────────────────────────┘
                           │
┌──────────────────────────▼─────────────────────────────────┐
│ Orchestrator + decider spine                               │
│   internal/orchestrator                                    │
│   internal/decider — Decision (5 values), Result, Env,     │
│   Combine (deny > ask > passthrough > allow > silent),     │
│   Allow/Deny/Ask/Silent helpers                            │
└────────────┬─────────────────────────┬─────────────────────┘
             │                         │
┌────────────▼──────────┐   ┌──────────▼──────────────────┐
│ Static-Bash decider   │   │ LLM-classifier decider      │
│ internal/staticbash   │   │ internal/llmclassifier      │
│   ast / rules /       │   │   phase pipeline,           │
│   cmdcheck / builtins │   │   backstop state,           │
│   policy              │   │   prompt builder,           │
│                       │   │   toolprep plugins,         │
│                       │   │   provider abstraction      │
└───────────────────────┘   └────┬────────────────────────┘
                                 │
                  ┌──────────────▼──────────────┐
                  │ Provider layer              │
                  │   providers/ (interface)    │
                  │   providers/bedrock/        │
                  │     (FromProto + Converse)  │
                  └─────────────────────────────┘

Crosscutting (used by the spine, both deciders, or the harness):
  internal/claudecode/{transcript, claudemd, permscope,
                       automodepolicy, paths}
  internal/config · internal/hookio · internal/decisionlog
  internal/cachepath · internal/pathutil · internal/logging
  internal/gen (proto-generated)
```

## Layer-By-Layer

### 1. Hook entrypoint — `cmd/claude-auto-permission`, `internal/app`

A single-shot CLI: read one `HookInput` from stdin, build the per-call `Env`, hand off to the orchestrator, exit.
`app` is intentionally thin — it owns wiring (config load, decider construction, decisionlog) but no policy.

### 2. Orchestrator + decider spine — `internal/orchestrator`, `internal/decider`

The architectural contract. `decider` defines:

- The five-valued `Decision` enum and `Result` shape.
- `Combine`, with precedence `deny > ask > passthrough > allow > silent`.
- The `Decider` interface + `Allow / Deny / Ask / Silent` constructors.
- `Env`, the per-call resolved bundle (cwd, project root, permission scope, settings candidates, resolved CLAUDE.md).

`orchestrator` runs every registered decider against the same input and `Env`, combines their votes, emits the wire
output (or stays quiet on `silent` / `passthrough`), and appends the JSONL audit line with a per-decider breakdown.

The split between `silent` ("no opinion") and `passthrough` ("should have opined, can't") is what makes peer deciders
compose safely under partial failure: a permissive `allow` from one decider must not stand when a peer that *should*
have opined is incapacitated.

Adding a third decider is a one-file change — the spine is content-free about how many deciders there are.

### 3. Two decider implementations

**Static-Bash** (`internal/staticbash`): an AST walker over `mvdan.cc/sh/v3/syntax`, organized as:

- `ast` — entry point + traversal.
- `rules` — engine + condition DSL, decoupled from `config.Resolver` via function-typed seams (`RemoteHostLookup`,
  etc.).
- `cmdcheck` — per-command shape checks.
- `builtins` — bundled checker implementations (sed, awk, and aliases).
- `policy` — write-allowed / remote-host queries lifted out of `config.Resolver`.

Microsecond cost; today only emits `allow` / `silent`. `deny` is reserved capability.

**LLM classifier** (`internal/llmclassifier`): a phase-based pipeline whose spine lives in `pipeline.go`:

```
checkInput
  → matchProjectConfig
  → checkBackstop
  → checkPermissionMode
  → checkSkippable
  → sanitizeProposedAction
  → loadTranscriptPhase
  → buildPromptPhase
  → resolveProvider
  → classifyAndRecord
```

Each phase returns `(Result, bool)`; the spine threads them and bails on the first stop. Failure paths split by *cause*:

- **Doesn't apply** (disabled, permission-mode skip, skip-list, empty input) → `silent`.
- **Should have classified but couldn't** (transcript read, prompt build, provider construction or call) →
  `passthrough` by default, or `ask` when `on_classifier_error: ASK` is configured.
- **Backstop trip** → `ask` unconditionally.

Sub-packages:

- `prompt` — system + user-prefix + transcript builder, JSONL serialization, sanitize-then-render flow.
- `state` — per-session block counters with flock'd RMW.
- `toolprep` — per-tool plugin surface (see below).
- `providers` — model abstraction (see below).

### 4. Per-tool plugin surface — `internal/llmclassifier/toolprep`

Owned entirely by the classifier. Each tool implements `Skippable` (skip-list logic — safe-tool allowlist, in-cwd
file ops, etc.) and `Sanitize` (project raw input down to the short string the classifier prompts on). One plugin per
category:

- `bash.go` — Bash (always classify; sanitize emits `command`).
- `files.go` — Read / Write / Edit / NotebookEdit (in-cwd fast-paths).
- `external.go` — WebFetch, WebSearch, Agent (always classify).
- `mcp.go` — `mcp__*` tools (sorted key=value flatten).
- `safe.go` — Grep, Glob, TodoWrite, Task management, MCP discovery, etc. (skip always).

Flat package, no nested subpackages. Static-Bash does **not** go through this layer — it consults
`internal/staticbash` directly.

### 5. Provider layer — `internal/llmclassifier/providers`

The classifier's model abstraction. Bedrock today (Converse API, two-stage modes, forced-tool-use single-stage). Each
provider owns its `FromProto` so config validation lives next to the provider that consumes it; the factory is a thin
dispatcher. Sized for Vertex / first-party Anthropic next.

### 6. Claude Code integration — `internal/claudecode/*`

Generic Claude Code behavior, reused across deciders and the surrounding harness:

- `transcript` — JSONL transcript reader, parent + subagent merging, sanitization.
- `claudemd` — CLAUDE.md walker + `@-import` resolver.
- `permscope` — canonical settings.json discovery (managed → user → project → local), working-dir union, deny-rule
  union, and the candidates list used as the auto-mode policy cache key.
- `automodepolicy` — `LoadOrDefaults(ctx)` shells out to `claude auto-mode config`, caches by binary + settings
  fingerprint, falls back to bundled defaults on error.
- `paths` — user-tier filesystem locations (settings.json, CLAUDE.md) derived from `HOME`, `CLAUDE_CONFIG_DIR`,
  `CLAUDE_PROJECT_DIR`.

This is the "what does Claude Code look like to us" layer — nothing here knows about the classifier, the static
engine, or the decider contract.

### 7. Cross-cutting infrastructure

- `config` — proto-loaded, glob-matched per project. Source of truth for "what does this project want?"
- `hookio` — wire JSON shapes for hook input + output.
- `decisionlog` — `O_APPEND`-atomic JSONL with flock'd rotation.
- `cachepath` — `~/.cache/claude-auto-permission/...` resolution.
- `pathutil` — generic path helpers (NFC normalization, tilde expansion).
- `logging` — structured logger with per-call context.
- `gen` — proto-generated code (`internal/gen/config/v1`).

Used by everything above; depends on nothing in the decider stack.

## Two Things Worth Highlighting About The Shape

### The classifier pipeline is data-shaped, not control-shaped

`runPipeline` is a `[]func(ctx, *step) (Result, bool)` loop. Each phase reads the `step` fields earlier phases
populated and writes the ones later phases consume. New phases slot in by appending to the slice; testing a phase in
isolation is trivial because the carrier struct is the only contract. Phase boundaries are also the unit of "is this
a doesn't-apply skip or an infra failure?" — the `onError` helper consults `on_classifier_error` from the per-project
config, so the policy lives in one place rather than at every failure site.

### Function-typed seams keep the rule engine context-free

The static-Bash rules engine evaluates conditions against parsed args without knowing about `config.Resolver`,
`paths.Resolve`, or the AST walker. Function-typed fields on its evaluation context (`IsPathAllowed`, `Evaluate`,
`RemoteHostLookup`) let the walker supply real implementations and tests pass closures. The rules package compiles
without depending on the project config, the walker, or any other domain glue — making the rule grammar reusable in
isolation and the test surface flat.
