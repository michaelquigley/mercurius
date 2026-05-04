# Mercurius — Work Order

This is the implementation plan. It assumes the design in [`design.md`](design.md) has been read.

## Tech stack

- **Language:** Go.
- **Logging:** `github.com/michaelquigley/df/dl`.
- **Marshaling:** `github.com/michaelquigley/df/dd` for YAML/JSON.
- **MCP:** the standard Go MCP server SDK (Anthropic's `mcp-go` or equivalent; pick at scaffold time, document the choice).
- **Build/test:** standard `go build` / `go test`. No additional build tooling unless something in M3+ requires it.

## Repo layout

```
mercurius/
  cmd/
    mercurius/
      main.go              # MCP server entrypoint
  internal/
    broker/                # session/round orchestration
    prompt/                # review prompt template + assembly
    reviewer/              # Reviewer interface + registry
      codex/               # codex reviewer implementation
      dummy/               # in-process dummy reviewer for tests
    schema/                # structured review output schema + validation
    log/                   # round log writer
    config/                # mercurius.yaml loading
  docs/
    design.md
    work-order.md
  examples/
    mercurius.yaml         # sample project config
  README.md
  LICENSE
  go.mod
  go.sum
```

`internal/` because no part of this is intended to be imported by other Go projects today. If a need surfaces later, individual packages can be promoted.

## Milestones

Five milestones. M1–M4 are the MVP. M5 is V2.

### M1 — Scaffolding and reviewer interface

**Scope:**

- Initialize Go module, repo layout, basic CI (lint + test on push).
- Define the `Reviewer` interface and supporting types per design §4.
- Define the structured review output schema per design §5, including JSON schema for validation.
- Implement a `dummy` reviewer that returns a hardcoded valid `ReviewResponse` for any input. Used in tests.
- Implement schema validation (load JSON schema, validate `Raw` against it, return descriptive errors).

**Definition of done:**

- `go build ./...` passes.
- `go test ./...` exercises the dummy reviewer end-to-end through schema validation.
- The schema is documented in `internal/schema/` with comments explaining each field.

### M2 — Codex reviewer implementation

**Scope:**

- Implement the `codex` reviewer satisfying `Reviewer`, following the V1 invocation spec in design §12.
- Wire the subprocess shape: `codex exec` with `--output-schema <tmp>` (the schema from the request, written to a temp file per round), `--output-last-message <tmp>` for response capture, the request's pre-assembled `Prompt` fed on stdin, read-only sandbox, no filesystem side effects.
- Read the captured last-message file, strip conventional wrappers (markdown fences, stray prose) if any survive `--output-schema`, return the bytes as `ReviewResponse.Raw`. Schema validation is the broker's job, not the reviewer's.
- The reviewer does not assemble prompts, does not own templates, does not read artifacts from disk on its own behalf. It runs codex with what it's given and returns what comes back.

**Definition of done:**

- The codex reviewer can be exercised against a real codex install and returns a populated `ReviewResponse` on representative input (prompt and schema supplied by the test).
- A short note under `internal/reviewer/codex/README.md` records the exact `codex exec` invocation, the temp-file lifecycle, and any flags or env vars that matter.
- Unit tests cover the subprocess wiring and output capture using fixture data; integration tests against a live codex are runnable but gated behind a build tag so CI can skip them.

### M3 — Session and round orchestration, prompt assembly, log writer

**Scope:**

- Implement the prompt assembly layer. The broker owns the standard review prompt template, the schema, and the assembly logic that combines them with the artifacts and session context (round number, prior decisions if available) to produce the `ReviewRequest.Prompt` string and `ReviewRequest.Schema` payload.
- Implement session management: open/close, in-memory state, round counter, budget enforcement.
- Track prior decisions in session state. Each `record_round_notes` call updates the session's accumulated decision list, which the prompt assembly layer reads to populate `SessionMeta.PriorDecisions` on the next round.
- Implement round execution: assemble request, dispatch to reviewer, validate the returned `Raw` against the schema (this is the broker's validation step, not the reviewer's), write log entry, return result.
- Implement the log writer with two operations: (1) write the initial round entry containing reviewer output(s); (2) update an existing round's log file with commentary and/or decisions sections, idempotently — subsequent calls for the same round replace those sections rather than appending duplicates.
- Define and document the round log markdown structure, including reserved section headings for commentary and decisions.

**Definition of done:**

- A session can be opened, run through several rounds against the dummy reviewer, and closed.
- The dummy reviewer receives a fully-assembled `ReviewRequest` (prompt and schema populated) on every call, demonstrating that prompt construction lives entirely in the broker.
- Each round produces a log file with the expected structure.
- Schema validation of reviewer output happens in the broker and surfaces as a clean error (not a panic) when the dummy reviewer is configured to return malformed output.
- Recording notes against a round populates the round's log file with commentary and decisions sections; recording notes a second time for the same round replaces those sections cleanly.
- A subsequent round dispatched after notes have been recorded sees the prior decisions in `SessionMeta.PriorDecisions`, demonstrating end-to-end carry-over.
- Budget enforcement is tested (a round attempted past the budget returns a clean error, not a panic).
- Log destination paths are validated at session-open time with descriptive errors for unwritable destinations.

### M4 — MCP server surface

**Scope:**

- Implement the MCP tools listed in design §7: `open_session`, `review_round`, `record_round_notes`, `close_session`, `session_status`, `list_sessions`.
- Implement project config loading (`mercurius.yaml`).
- Wire the server entrypoint at `cmd/mercurius/main.go` with stdio transport.
- Document install and usage in the README.

**Definition of done:**

- A design agent (Claude in a chat) can connect to a locally-running Mercurius via MCP, open a session against a real project's design + work-order files, run a round, record notes (commentary and decisions) for that round, run a follow-up round that surfaces the prior decisions to the reviewer, and close the session.
- The example `mercurius.yaml` exercises both single-reviewer and override-prompt configurations.
- README has an install section with a working example invocation.

### M5 — Panel mode and diff rounds (V2)

**Scope:**

- Extend session config to allow N reviewers per round.
- Run reviewers in parallel; aggregate results without merging.
- Extend the log entry format to handle multiple reviewer outputs cleanly.
- Implement diff rounds: a round type that takes both the round-0 and current artifact sets and runs a special review prompt asking what's been lost.

**Definition of done:**

- Panel mode can be configured per session and produces a round result containing all reviewer outputs distinctly.
- A diff round can be requested explicitly via `review_round(session_id, diff: true)` and produces output identifiable as a diff round in the log.

Deferred to V2 because the MVP loop is valuable without these, and shipping the simpler version first surfaces real-use feedback that should inform the panel and diff designs.

## Test strategy

Three test layers:

- **Unit tests** for schema validation, prompt assembly, log writing, config loading, and budget enforcement. Fast, no external dependencies, run on every CI build.
- **Integration tests** for the codex reviewer against a live codex install. Gated behind a `//go:build integration` tag so CI runs them only on demand. A small fixture project (a fake design doc and work order) lives under `testdata/` for repeatable runs.
- **End-to-end smoke** as part of M4 acceptance: a script that launches Mercurius, exercises the MCP surface, and asserts on the resulting log files. Not in CI; documented under `examples/` for manual verification.

No mock for the MCP transport in unit tests; the broker logic is tested directly without going through MCP.

## Sequencing notes

- M1 and M2 can be parallelized if needed, but M2 depends on the schema definition from M1, so M1 should land first.
- M3 depends on M1 (to have a Reviewer to call). It can be built against the dummy reviewer and integrated with codex once M2 lands.
- M4 is the integration milestone; it does not start in earnest until M1–M3 are stable.
- M5 should not be started until the MVP has been used for at least one real review loop. Real-use feedback will reshape its design.

## Things deliberately not in scope for V1

Listed so they are not accidentally added under cover of "while we're here":

- Headless / CI-driven Mercurius. Chat-driven only.
- Web UI for browsing sessions or logs. Logs are markdown files; existing tooling handles them.
- Database-backed session storage. In-memory only.
- Authentication or multi-user support. Local single-user only.
- Reviewer impls beyond codex and dummy. Designed to support more; not implementing them in V1.
