# AGENTS.md

This file provides guidance to AI agents (Claude Code, Codex, Cursor, etc.) when working in this repository.

Read README.md for high level information about the project and DEVELOPMENT.md.

## Architectural Overview

See:

- docs/design.md — overall hook architecture, decision pipeline, caching, concurrency, logging.
- docs/static-bash-rules-design.md — static Bash AST walker and rule DSL.
- docs/llm-classifier-design.md — LLM classifier subsystem.
- docs/code-architecture.md — package-level code map.

## Code Style

DRY, but not prematurely (YAGNI). Only when a pattern / workflow is multiply repeated or overly complicated and
readability could be helped should you break things up into logical components and *tasteful* (KISS) abstractions,
refactoring existing code if necessary.

Code should be organized into logical components using packages and interfaces, separating concerns. Components should
be composable and testable in isolation.

### Comments

- Comments should explain *why*, not *what*.
- Focus on non-obvious details, relevant design, intentional tradeoffs, or important context.
- Do not add comments that just summarize or repeat the code.
- If the code is self-explanatory, no comment is needed.
- Do not add change commentary (retrospective or otherwise). Comments are not a work log: no "previously…", "used to…",
  "now does…", "moved from…", "removed X", "follow-up", etc. Document the code as it stands, in the present tense.
  Change history belongs in the commit / PR, not in the source.

### Variables

- Inline single use variables at their use sites if the expression is straightforward.
- Intermediate variables are useful when they clarify intent or when the expression is complex.
- Prefer defining and documenting constants over inline use of constant literals. The exception is for simple, trivial
  constants (e.g., `0`) if their use is self-explanatory.

### Control Flow

Prefer the early return / guard clauses pattern over deeply nested `if` statements.

## Tests

Always add or update tests when appropriate for a change.

The majority of your tests should be unit tests covering representative, realistic flows, as well as representative and
important edge cases.

Tests should be human-readable and easy to follow along, and easily understood by a reader to be correct by visual
inspection. So don't add too many abstractions or too much indirection in your tests.

For test cases covering one logical concept but with multiple potential sub-cases to be covered, follow the
[subtest pattern](https://go.dev/blog/subtests) to parameterize a test case.

Follow the "arrange, act, assert" pattern.

After unit tests, the minority of your tests should be integration tests.

### Mocks

Use mocks where appropriate in tests. Don't manually edit generated mock files, run `make gen` to regenerate them when
an interface changes.

## Security

Always check both in new and preexisting code for potential security issues, e.g., SQL injection, IDOR, SSRF, other
confused deputy or abuse of server ambient authority issues, code injection / code execution, even side-channels.

Adhere to best practices and defense-in-depth.

## Workflow Rules

Make sure you explore the code base and understand it before jumping into planning or coding.

Prefer using the LSP plugins to navigate the code rather than `grep`ing or `find`ing symbols.

Explore, interview and ask clarifying questions (if necessary), *then* plan. If there are open questions and multiple
approaches, make a recommendation and explain tradeoffs.

### Making changes

- Prefer using built-in tools like `Read`, `Grep`, `Glob`, `Edit`, `Update` for simple find / read / edit operations
  over calling bash / shell commands like `cat`, `grep`, `find`, `sed`.
- Always format (e.g, `go fmt`, etc.) and lint changed files.
- Always run all tests for affected packages.
- **Never commit directly to the main branch.** Always create a feature branch and open a PR.
  - Commit messages must be concise, high level summaries.
    Follow [Conventional Commits](https://conventionalcommits.org) style.
- When working on multiple changes in parallel, use Git worktrees. Clean up worktrees after a PR is merged.

### Submitting changes

Your commit messages and PR descriptions must be **concise**, **high level** summaries, and contain only the most
salient information a reader should know.

An *example* of a PR description:

```markdown
## Summary

**Short, concise, HIGH LEVEL** summary of the change.

## Context

Provide context, describe the problem, high level goal, and motivating factors.

## Approach

Summarize any key design decisions made and any principles involved, alternatives explored and any tradeoffs or
limitations, if applicable.

## Key Changes

Summarize the key changes—existing behavior if relevant, new behavior. Keep it big picture, do not go into
implementation details, do not excerpt actual code changes.

DO NOT ADD EXTRA FLUFF ABOUT: "Added: full test coverage" or "Test plan: add unit tests, all tests passed."
It is taken for granted every change comes with relevant tests, and they all pass.
Only call out changes to tests if it's really important to know, e.g., fixing broken or incorrect tests, fixing gaps,
etc.

## Follow-ups

Optional, only if relevant.
```

For each of these sections, only use them if they're relevant to the change.

DO NOT go into specific lines of code, specific files or line numbers. Avoid going into implementation details like
specific classes or methods unless it's really important to know. Keep it high level, focused on high level concepts,
semantics, logical components of the systems being modified, and external systems if involved.
