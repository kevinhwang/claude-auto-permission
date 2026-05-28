# Development

Notes for working on claude-auto-permission itself. For *using* the tool, see [`README.md`](README.md) and
[`GETTING_STARTED.md`](GETTING_STARTED.md). For design context before making non-trivial changes, see
[`docs/design.md`](docs/design.md).

## Setup

Prerequisites:

- [**Go**](http://golang.org)
- [**Make**](https://www.gnu.org/software/make/manual/make.html): For running Makefile targets
- [**Buf**](https://buf.build): for regenerating [proto](https://protobuf.dev) bindings.
- [**jq**](https://jqlang.org): for the install-hook script and for inspecting decision logs.

Clone, then:

```shell
make build           # builds ./build/claude-auto-permission
make test            # runs the full unit test suite
```

## Code Layout

The package-level map lives in [`docs/code-architecture.md`](docs/code-architecture.md). At a glance:

- [`cmd/claude-auto-permission`](cmd/claude-auto-permission) — entrypoint.
- [`internal/app`](internal/app) — wiring (config load, decider construction, decision log).
- [`internal/orchestrator`](internal/orchestrator) — runs deciders, combines votes, emits wire output.
- [`internal/decider`](internal/decider) — the decider contract (votes, combiner, env).
- [`internal/staticbash`](internal/staticbash) — the static Bash AST walker and rule engine.
- [`internal/llmclassifier`](internal/llmclassifier) — the LLM classifier subsystem.
- [`internal/claudecode`](internal/claudecode) — Claude Code integration glue (transcript reader, CLAUDE.md walker,
  permission-scope resolver, auto-mode policy loader).
- [`internal/config`](internal/config), [`internal/hookio`](internal/hookio),
  [`internal/decisionlog`](internal/decisionlog), [`internal/cachepath`](internal/cachepath),
  [`internal/pathutil`](internal/pathutil), [`internal/logging`](internal/logging) — cross-cutting infrastructure.
- [`internal/gen`](internal/gen) — proto-generated code (do not edit).

## Building & Running

```shell
make build           # build into ./build/claude-auto-permission
make install         # go install to ${GOPATH}/bin
make install-hook    # interactive end-to-end install (binary, config, hook registration)
make clean           # rm -rf ./build
```

To smoke-test the binary by hand, pipe a synthetic hook event at it:

```shell
echo '{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git status"},"cwd":"/some/project"}' \
  | ./build/claude-auto-permission
```

The binary reads one event from stdin and writes the decision (or nothing) to stdout. It **never** executes the
command in `tool_input` — even so, when constructing test inputs, avoid embedding obviously destructive commands
inside the JSON with weird escapes, e.g., `echo "$(rm -rf /)`. A typo in shell quoting could leak the inner command out
of the JSON literal and execute it before the binary ever sees it.

## Testing

```shell
make test            # all unit tests
make e2e             # quick-tagged subset of e2e tests (LLM cases hit real Bedrock)
make e2e-full        # full e2e suite
```

### Unit tests

`make test` runs the standard `go test ./...`. Most packages have unit tests next to the code they cover. The walker,
rule engine, classifier pipeline, prompt builder, and provider parsers all have substantial coverage.

A few packages have **cross-process** tests (`xprocess` build tag) that spawn multiple binary instances to verify
file-locking invariants under real concurrent contention. These are more expensive to run; the default test target
includes them via build tags where appropriate. To run just the cross-process suite:

```shell
go test -tags xprocess ./internal/decisionlog/...
go test -tags xprocess ./internal/llmclassifier/state/...
```

### End-to-end conformance suite

`test/e2e/` is a conformance test harness driven by [txtar](https://pkg.go.dev/golang.org/x/tools/txtar) case files.
Each case bundles:

- `case.yaml` — description, tool, expected verdict, optional tags.
- `tool_input.json` — the proposed tool input.
- `transcript.jsonl` — optional session transcript for classifier cases.
- `config.txtpb` — optional config; when absent, the suite supplies a sensible default.
- Other support files as needed (e.g. an effective auto-mode policy snapshot for classifier cases).

Three sub-suites live under `test/e2e/`:

- **`smoke/`** — fast, hermetic. No external dependencies.
- **`bash/`** — exercises the static Bash walker against representative real-world commands.
- **`classifier/`** — exercises the LLM classifier against curated should-allow / should-deny cases.

The classifier subsuite **hits real Bedrock** — it costs money to run, and requires valid AWS credentials in the
ambient environment. The default (`make e2e`) runs the `quick`-tagged subset; `make e2e-full` runs everything.

To run a single classifier case during development:

```shell
CLAUDE_AUTO_PERMISSION_E2E=1 go test -run 'TestClassifier/should_approve/curl_rustup' ./test/e2e/classifier/
```

### Contributing to the conformance corpus

The corpus is the canonical regression-test surface. Adding cases is encouraged, especially when:

- A real-world command tripped a false positive (the walker fell through on something obviously safe).
- A real-world command tripped a false negative (auto-approved something that should have been gated).
- The classifier missed a denial it should have caught (e.g. a user boundary phrased unusually).
- A new tool or rule pattern needs coverage.

For static-Bash cases, drop a new `.txtar` into `test/e2e/bash/cases/` matching the pattern of the existing files.
The case description should make it clear what the case is testing and why.

For classifier cases, the directory structure under `test/e2e/classifier/cases/` reflects taxonomies (`should_approve`,
`destructive`, `exfil`, `obfuscation`, …). Keep the transcript minimal — the goal is to test classifier judgment on
representative shapes, not to bloat the test suite. Tag cases with `quick` if they should run in `make e2e` (the
default fast subset).

## Mocks & Code Generation

`make gen` regenerates proto bindings and gomock-generated test doubles:

```shell
make proto           # buf generate (proto bindings only)
make gen             # proto + go generate (proto + mocks)
```

Don't edit `internal/gen/...` or `mocks/...` files by hand — change the source proto / interface and re-generate.

After modifying any `.proto` file:

```shell
buf format -w        # format
make proto           # regenerate bindings
```

(See [`proto/AGENTS.md`](proto/AGENTS.md) for the full proto workflow.)

## Formatting & Linting

Standard Go tooling:

```shell
go fmt ./...
go vet ./...
```

CI runs both via [`/.github/workflows/ci.yml`](.github/workflows/ci.yml). Format changed files before submitting.

## Debugging

The decision log is the first place to look for "why did the hook do X?":

```shell
tail -F ~/.cache/claude-auto-permission/decisions.log.jsonl | jq --unbuffered -C .
```

Every per-decider entry carries a `reason` field — for `silent` and `passthrough` verdicts especially, the reason is
the highest-value debug signal. It explains *why* a decider stayed quiet (skip-list hit, backstop tripped, provider
error, not the right tool, …).

For classifier-specific debugging, set `runtime.dump_llm_classifier: true` in your config. The hook will write the
full provider request and response — including the rendered system prompt — to
`~/.cache/claude-auto-permission/dumps/` for every classifier call. Useful for prompt-engineering work or filing
reproducible bug reports.

## Filing Bugs

When opening an issue, include where possible:

- The decision log line for the failing call (with sensitive paths/contents redacted).
- A txtar-style minimal reproducer if the bug is in the walker or the classifier.
- The version of `claude` (`claude --version`) for classifier issues — auto-mode policy is sourced from there.
- The Bedrock model ID for classifier issues.
