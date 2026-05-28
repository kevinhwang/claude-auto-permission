# Getting Started

## Prerequisites

- **macOS or Linux.** No Windows support today (the hook uses POSIX file locks and signal handling).
- [**Go**](http://golang.org)
- [**Make**](https://www.gnu.org/software/make/manual/make.html)
- [**jq**](https://jqlang.org): Optional, only used by `make install-hook` to edit your Claude Code `settings.json`
  files for you. You can still run the hook without `jq`, but you'll need to register the hook manually (see below).
- **`claude` CLI on your `PATH`** if you plan to enable the LLM classifier

> [!IMPORTANT]
> Make sure your `${GOPATH}/bin` is on the `${PATH}` Claude Code itself runs under. Otherwise, Claude Code can't find
> the binary, and you'll get a silent "command not found" with no decisioning at all.

## Build & Install

The fastest path is a single command:

```shell
make install-hook
```

This runs an interactive script that:

1. Builds and installs the binary to `${GOPATH}/bin` via `go install`.
2. Creates a starter config at `~/.config/claude-auto-permission/config.txtpb` (only if the file doesn't already exist).
3. Registers a `PreToolUse` hook in `~/.claude/settings.json` (only if not already registered, and only if `jq` is
   available).

Each step prompts before doing anything destructive — you can decline any step and re-run later.

If you'd rather do it by hand:

```shell
make install        # builds and installs the binary only
```

## Register The Hook

If you want to register the hook manually (e.g. you want to customize the env vars it runs under, like setting an
`AWS_PROFILE` for the LLM classifier), add this to your `~/.claude/settings.json`:

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "*",
        "hooks": [
          {"type": "command", "command": "claude-auto-permission"}
        ]
      }
    ]
  }
}
```

To pin AWS config for the classifier without polluting your shell environment, prefix the command:

```json
"command": "AWS_PROFILE=claude AWS_REGION=us-east-1 claude-auto-permission"
```

## Configuring

Configuration lives in a [text-format proto](https://protobuf.dev/reference/protobuf/textformat-spec/) file at
`~/.config/claude-auto-permission/config.txtpb` (override the path with `CLAUDE_AUTO_PERMISSION_CONFIG`).

The full schema with field-level documentation is in [`proto/config/v1/config.proto`](proto/config/v1/config.proto) —
treat that as the authoritative reference. The summaries below are deliberately brief.

### Global Settings

A small set of process-level knobs under the top-level `runtime { ... }` block. The most useful ones:

| Field                 | Default                            | Notes                                                                |
|-----------------------|------------------------------------|----------------------------------------------------------------------|
| `cache_dir`           | `~/.cache/claude-auto-permission/` | Decision log, session counters, auto-mode policy cache, debug dumps. |
| `claude_config_dir`   | `~/.claude`                        | Claude Code's user-tier dir. Honors `CLAUDE_CONFIG_DIR` env var.     |
| `dump_llm_classifier` | `false`                            | Dump every classifier provider call to `<cache_dir>/dumps/`.         |

For everything else (including overrides) see the proto.

### Bash Rules Settings

Per-project, under `static_bash_rules { ... }`. The two pieces most users touch:

- **`use_default_rules {}`** — opt into the bundled rule set covering ~80 common dev tools. This is the typical
  starting point. (Mutually exclusive with `custom_command_rules`.)
- **`allow_write_patterns: "..."`** — glob patterns marking paths Claude is allowed to write to. `~` and `**` are
  expanded.

A couple of less common but useful toggles: `allow_project_write` (writes anywhere under `$CLAUDE_PROJECT_DIR`),
`remote_hosts { ... }` (per-host write scopes for `ssh`/`tsh` recursion).

To replace the defaults entirely with your own DSL rules, use `custom_command_rules { rule_set { ... } }`. The grammar
is in [`proto/rules/v1/rules.proto`](proto/rules/v1/rules.proto); the bundled defaults at
[`internal/staticbash/rules/default.txtpb`](internal/staticbash/rules/default.txtpb) are by far the best example
corpus.

A trivial custom rule for illustration:

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
                allowed_with_any_args: ["status", "version"]
              }
            }
          }
        }
      }
    }
  }
}
```

Allows `mytool status` and `mytool version` with any further args; everything else falls through.

### LLM Classifier Settings

> [!NOTE]
> **Precedence:** the classifier overrules the static Bash layer. When the classifier denies (e.g. it sees a
> "don't push to main" boundary in the transcript), it wins even if the static layer matched its allowlist. This is
> the headline scenario the architecture exists for. Practically, when the classifier is enabled, the static rules are
> redundant for *deny* outcomes — they don't hurt, but they don't independently change the final verdict. They still
> contribute on the allow path: a static allow lets the classifier's allow stand without needing to reason about it.

#### Provider

Only **Amazon Bedrock** is supported today. AWS config and credentials resolve through the AWS SDK's default chain
(env vars, `~/.aws/`, IMDS, SSO). To pin a profile or region just for this hook, prefix the hook `command` in
`settings.json` (see above).

A sample Bedrock config using a global cross-region inference profile:

```txtpb
projects {
  path_patterns: "~/src/myproject/**"

  llm_classifier {
    enabled: true
    bedrock {
      model_id: "global.anthropic.claude-sonnet-4-6"
    }
    timeout_ms: 20000
    log_decisions: true
  }
}
```

#### Two-Stage Modes

Bedrock supports four classification strategies trading latency for reasoning depth:

| Mode                         | What it does                                                                                                         | When to pick                                                                                            |
|------------------------------|----------------------------------------------------------------------------------------------------------------------|---------------------------------------------------------------------------------------------------------|
| `TWO_STAGE_BOTH` *(default)* | Stage 1 streams XML to a quick yes/no; on `yes`, stage 2 escalates to chain-of-thought for the authoritative reason. | Best balance of correctness and latency. Matches official Auto Mode's production default.               |
| `TWO_STAGE_FAST`             | Stage 1 only.                                                                                                        | Fastest. Higher false-positive rate on nuanced cases — no chain-of-thought reasoning about user intent. |
| `TWO_STAGE_OFF`              | Single forced-tool-use call against a JSON Schema.                                                                   | Legacy. Haiku 4.5 has a known issue with this mode (drops the verdict boolean from structured output).  |
| `TWO_STAGE_THINKING`         | Single XML chain-of-thought call.                                                                                    | Useful as an A/B baseline against the two-stage modes.                                                  |

Set via `bedrock { two_stage: TWO_STAGE_FAST }`.

#### Customizing Allow / SoftDeny Rules and Trust Bullets

The classifier's policy (allow exceptions, soft-deny rules, hard-deny rules, trusted-domain environment bullets) comes
from `claude auto-mode config` — Claude Code's own merge of shipped defaults with user `autoMode.*` overrides. To
customize, edit `autoMode.{allow, soft_deny, environment}` in:

- `~/.claude/settings.json` — applies to every project.
- `<project>/.claude/settings.local.json` — project-scoped (gitignored by default).

Same surface official Auto Mode reads. See [`docs/llm-classifier-design.md`](docs/llm-classifier-design.md) for why
project-tier `<cwd>/.claude/settings.json` is intentionally **not** read for `autoMode.*`.

## Debugging

### Decision Log

When any project has `log_decisions: true`, every tool-call decision appends a JSONL line to
`~/.cache/claude-auto-permission/decisions.log.jsonl`. Tail it live to watch the hook think:

```shell
tail -F ~/.cache/claude-auto-permission/decisions.log.jsonl | jq --unbuffered -C .
```

Each line carries the orchestrator's combined verdict at the top level plus a per-decider breakdown so you can see
*which* layer voted what — and crucially, why a silent decider stayed silent. That's where most "why didn't it
auto-approve?" mysteries get solved.

### Classifier Dumps

If you set `runtime.dump_llm_classifier: true`, the hook writes the full provider request and response (including the
fully-rendered system prompt) for every classifier call to `~/.cache/claude-auto-permission/dumps/`. Useful for
prompt-engineering or filing reproducer issues.

### Smoke-Testing The Hook

Drive the binary directly to confirm it's wired up correctly:

```shell
echo '{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"git status"},"cwd":"/some/project"}' \
  | claude-auto-permission
```

The hook reads one event from stdin and writes the decision (or nothing, on silent / passthrough) to stdout. An empty
stdout means "no opinion — fall through to Claude Code's normal permission flow." The binary never executes the
command in `tool_input`; it only decides whether Claude Code is allowed to.
