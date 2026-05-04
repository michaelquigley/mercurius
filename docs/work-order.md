# Mercurius — Work Order

This is the implementation plan. It assumes the design in [`design.md`](design.md) has been read.

## Tech stack

Mercurius follows the metawoo Go conventions used across peer projects (`archive`, `lore`, `sexton`, `pane`):

- **Language:** Go 1.26+.
- **CLI:** `github.com/spf13/cobra` for command and flag handling.
- **Config:** `github.com/michaelquigley/df/dd` for YAML binding, defaults, and validation. `dd` auto-converts CamelCase struct fields to snake_case YAML keys; struct tags are only needed for `+required`, `+extra`, or explicit name overrides.
- **Logging:** `github.com/michaelquigley/df/dl` (structured slog wrapper).
- **MCP:** `github.com/modelcontextprotocol/go-sdk/mcp` — the official Model Context Protocol Go SDK. Locked in for V1 to align with the canonical MCP implementation and remove ambiguity at scaffold time.
- **Lifecycle:** `github.com/michaelquigley/df/da` is available for container/start/stop wiring if it fits naturally. Mercurius's V1 shape is simple (single stdio MCP server, in-memory session state) and may not need it; either approach is acceptable. If the implementing agent reaches for goroutine-based concurrency or multi-component lifecycle, `da` is the right choice.
- **Build/test:** standard `go build` / `go test`. No additional build tooling unless something in M3+ requires it.

## Project conventions

These rules come from the metawoo standard (see `pane/AGENTS.md`, `sexton/CLAUDE.md` for canonical statements). They apply throughout the codebase:

- In Go code, all comments start with a lowercase letter, unless the first word is a Go type that begins with an uppercase letter.
- All outputs logged or otherwise emitted to a user prefer lowercase, unless they reference a type that requires uppercase letters. Dynamic data in outputs appears between single quotes (e.g. `the session 's_xK3p9q' was closed`).
- Go files are named like `dashManager.go`, not `dash_manager.go`. Unit tests are named `dashManager_test.go`.
- Never use emoji in code, comments, or output.
- Clean up build artifacts (binaries, test executables) created during development or testing. Do not leave compiled binaries in the repository.
- Use mermaid diagrams in markdown documents instead of ASCII art.
- Diagrams should be mermaid; documents should be markdown.

These rules belong in an `AGENTS.md` at the repo root, mirroring the format used by peer projects, so they remain visible to future agent-led work without requiring this work order to be re-read. Creating that file is part of M1.

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
    roundlog/              # round log writer (named to avoid collision with stdlib `log`)
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

- Initialize Go module, repo layout, basic CI (`go vet ./...` and `go test ./...` on push — no extra lint tooling beyond the Go toolchain).
- Create `AGENTS.md` at the repo root mirroring the format used by peer projects (`pane/AGENTS.md`, `sexton/CLAUDE.md`): brief project orientation, package structure, tech stack, and the project conventions listed above.
- Define the `Reviewer` interface and supporting types per design §4.
- Define the structured review output schema per design §5, including JSON schema for validation.
- Implement a `dummy` reviewer that returns a hardcoded valid `ReviewResponse` for any input. Used in tests.
- Implement schema validation (load JSON schema, validate `Raw` against it, return descriptive errors).

**Definition of done:**

- `go build ./...` passes.
- `go vet ./...` passes with no warnings.
- `go test ./...` exercises the dummy reviewer end-to-end through schema validation.
- The schema is documented in `internal/schema/` with comments explaining each field.
- `AGENTS.md` exists at the repo root and is consistent with the conventions in this work order.

### M2 — Codex reviewer implementation

**Scope:**

- Implement the `codex` reviewer satisfying `Reviewer`, following the V1 invocation spec in design §13.
- Wire the subprocess shape: `codex exec -C <session_dir> --ephemeral --sandbox read-only --output-schema <tmp> --output-last-message <tmp>` with the schema and last-message paths written to a temp file per round, the request's pre-assembled `Prompt` fed on stdin, no filesystem side effects.
- Read the captured last-message file, strip conventional wrappers (markdown fences, stray prose) if any survive `--output-schema`, return the bytes as `ReviewResponse.Raw`. Schema validation is the broker's job, not the reviewer's.
- The reviewer does not assemble prompts, does not own templates, does not read artifacts from disk on its own behalf. It runs codex with what it's given and returns what comes back.

**Definition of done:**

- The codex reviewer can be exercised against a real codex install and returns a populated `ReviewResponse` on representative input (prompt and schema supplied by the test).
- A short note under `internal/reviewer/codex/README.md` records the exact `codex exec` invocation, the temp-file lifecycle, and any flags or env vars that matter.
- Unit tests cover the subprocess wiring and output capture using fixture data; integration tests against a live codex are runnable but gated behind a build tag so CI can skip them.

### M3 — Session and round orchestration, prompt assembly, log writer

**Scope:**

- Implement the prompt assembly layer per design §6's "The standard review prompt" subsection. The broker owns the V1 template (verbatim, with the documented substitution markers), the §5 schema, and the assembly logic that fills in artifacts, prior decisions, and `prompt_overrides` to produce the `ReviewRequest.Prompt` string and `ReviewRequest.Schema` payload. The required-sections list governs any future template revisions.
- Implement session management: open/close, in-memory state, round counter, budget enforcement.
- Track prior decisions in session state. Each `record_round_notes` call updates the session's accumulated decision list, which the prompt assembly layer reads to populate `SessionMeta.PriorDecisions` on the next round.
- Implement artifact snapshotting per design §7. At the start of each round, copy each artifact (or its inline `Content`) to `<session_dir>/snapshots/round-NN/<artifact-name>`, compute SHA-256 hash and byte size, and rewrite the `Artifact.Path` handed to the reviewer to point at the snapshot. Refuse to open a session if two artifacts share a logical name.
- Implement round execution: snapshot artifacts, assemble request, dispatch to reviewer, validate the returned `Raw` against the schema (this is the broker's validation step, not the reviewer's), write log entry, return result.
- Implement atomic round failure per design §6: if any step after snapshotting fails (reviewer error, schema violation, or other orchestrator fault), delete the round's snapshot directory, do not advance the round counter, do not consume budget, do not write a log entry. Return the error to the caller with full diagnostic content.
- Implement the log writer with two operations: (1) write the initial round entry containing the artifact manifest (per-artifact `name`, `source_path`, `snapshot_path`, `size`, `hash`) and reviewer output(s); (2) update an existing round's log file with commentary and/or decisions sections, idempotently — subsequent calls for the same round replace those sections rather than appending duplicates. The artifact manifest is written once and is immutable thereafter.
- Define and document the round log markdown structure, including the artifact manifest section and the reserved section headings for commentary and decisions.

**Definition of done:**

- A session can be opened, run through several rounds against the dummy reviewer, and closed.
- The dummy reviewer receives a fully-assembled `ReviewRequest` (prompt and schema populated) on every call, demonstrating that prompt construction lives entirely in the broker.
- Each round produces a log file that conforms to the round log file format defined in design §7: YAML frontmatter with the documented fields, artifact manifest table, per-reviewer H3 subsections under "Reviewer outputs", and the `mercurius:notes-begin` / `mercurius:notes-end` markers wrapping placeholder Commentary and Decisions sections. The format is verified by parsing the produced file in tests, not by string-matching prose.
- Each round produces a snapshot directory containing exact byte-copies of every input artifact; the recorded manifest in the log file matches the snapshot files (correct paths, sizes, hashes).
- Editing a source artifact between two rounds produces two distinct snapshot byte-streams and two distinct hashes in the log; round-1's log still resolves to round-1's bytes.
- Schema validation of reviewer output happens in the broker and surfaces as a clean error (not a panic) when the dummy reviewer is configured to return malformed output.
- Atomic round failure is exercised: a round forced into failure (via a dummy reviewer configured to error, return malformed output, or omit a required field) leaves no log file, leaves no snapshot directory on disk, does not advance the round counter, and does not decrement budget. A subsequent successful round reuses the same round number.
- Recording notes against a round replaces the mutable region between the markers cleanly. Recording notes a second time replaces it again without leaving stale content from the first call. The artifact manifest, reviewer outputs, and frontmatter (other than `notes_recorded`) are unaffected by either call.
- A subsequent round dispatched after notes have been recorded sees the prior decisions in `SessionMeta.PriorDecisions`, demonstrating end-to-end carry-over.
- Budget enforcement is tested (a round attempted past the budget returns a clean error, not a panic).
- Log destination paths are validated at session-open time with descriptive errors for unwritable destinations.

### M4 — MCP server surface

**Scope:**

- Implement the MCP tools listed in design §8: `open_session`, `review_round`, `record_round_notes`, `close_session`, `session_status`, `list_sessions`.
- Enforce the V1 single-reviewer constraint at `open_session`: if the resolved reviewer set has more than one entry (or zero, when the config has multiple entries with no caller-supplied selection), return `panel_mode_unsupported`.
- Implement project config loading (`mercurius.yaml`).
- Wire the server entrypoint at `cmd/mercurius/main.go` with stdio transport.
- Document install and usage in the README.

**Definition of done:**

- A design agent (Claude in a chat) can connect to a locally-running Mercurius via MCP, open a session against a real project's design + work-order files, run a round, record notes (commentary and decisions) for that round, run a follow-up round that surfaces the prior decisions to the reviewer, and close the session.
- The example `mercurius.yaml` exercises both single-reviewer and override-prompt configurations.
- A test exercises `open_session` against a config with two reviewers: omitting `reviewers` returns `panel_mode_unsupported`; supplying `reviewers: ["codex"]` succeeds; supplying `reviewers: ["codex", "dummy"]` returns `panel_mode_unsupported`.
- A test exercises artifact-name validation: names with slashes, `.`, `..`, characters outside `^[A-Za-z0-9._-]+$`, or length outside 1-64 are rejected with `invalid_artifacts`.
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
