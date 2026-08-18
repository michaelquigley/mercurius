# Mercurius

Workflow broker for design-agent review loops. Mercurius runs as a local MCP server, snapshots design artifacts, dispatches them to a configured reviewer, validates structured review output, and writes durable round logs.

## project context

See `docs/current/` for documentation that describes the current implementation. Start with `docs/current/README.md` for the current docs index and `docs/current/user-guide.md` for real-world usage.

See `docs/future/` for deferred designs, speculative changes, and notes about behavior that is not implemented yet. Do not document future behavior in `docs/current/` until the code exists and tests cover the behavior.

M1-M4 established the Go scaffold, reviewer interface, structured review schema, Codex subprocess reviewer, broker/session orchestration, prompt assembly, snapshotting, log writing, dummy reviewer test support, configuration loading, MCP stdio tool surface, background review rounds, CLI monitoring, review context, and advisory notes. The model was later simplified to single-shot rounds grouped under a lightweight session container; decisions log carry-forward, the convergence signal, the per-session budget, and the close_session verdict were removed in that pass.

## tech stack

- **language**: Go 1.26+
- **CLI**: github.com/spf13/cobra
- **config**: github.com/michaelquigley/df/dd
- **logging**: github.com/michaelquigley/df/dl
- **MCP**: github.com/modelcontextprotocol/go-sdk/mcp
- **schema validation**: github.com/santhosh-tekuri/jsonschema/v6
- **build/test**: standard `go build`, `go vet`, and `go test`

## package structure

- `cmd/mercurius/` - binary entrypoint
- `internal/broker/` - in-memory session and round orchestration
- `internal/config/` - `mercurius.yaml` loading, path resolution, and validation
- `internal/mcpserver/` - MCP server construction, tool registration, and error mapping
- `internal/prompt/` - standard review prompt assembly
- `internal/reviewer/` - Reviewer interface and shared request/response types
- `internal/reviewer/codex/` - Codex subprocess reviewer implementation
- `internal/reviewer/dummy/` - in-process reviewer for tests
- `internal/roundlog/` - markdown round log writer and notes updater
- `internal/schema/` - structured review output JSON Schema and validation
- `docs/current/` - complete documentation for the behavior implemented today
- `docs/future/` - future-facing specs, work orders, ideas, and deferred designs
- `README.md` - user-facing overview

## documentation rules

1. `docs/current/` is the source of truth for current behavior. Keep it accurate when changing user-facing tools, config, schema, session behavior, reviewer behavior, logs, or monitor output.

2. `docs/future/` is for planned or possible changes only. Future docs must clearly avoid implying that the behavior exists today.

3. When a future feature is implemented, re-synthesize the relevant `docs/future/` material into the `docs/current/` documentation as part of the implementation. Prefer a holistic merge that updates the current user guide, architecture, config, tool, and operations docs as appropriate; directly moving a future doc is only right when it already matches the current-doc structure and voice. Leave only still-deferred material in `docs/future/`.

4. Keep `README.md` concise. It should orient users and link to `docs/current/` and specific high-value guides rather than duplicating full reference material.

## project rules

1. in Go code, all comments start with a lowercase letter, unless the first word refers to a Go type that starts with an uppercase letter.

2. all outputs logged or otherwise emitted to a user prefer lowercase, unless they reference a type that requires uppercase letters. dynamic data in outputs appears between single quotes, like "the session 's_xK3p9q' was closed".

3. Go files are named like `dashManager.go`, not `dash_manager.go`. unit tests are named `dashManager_test.go`.

4. never use emoji in code, comments, or output.

5. clean up build artifacts, binaries, and test executables created during development or testing. do not leave compiled binaries in the repository.

6. use mermaid diagrams in markdown documents instead of ASCII art.

7. `dd` struct tags are only needed for `+required`, `+extra`, or name overrides. `dd` converts CamelCase fields to `snake_case` YAML keys automatically.

## Roadmap

This repo's roadmap lives in `docs/future/roadmap/` — one frontmatter-markdown
item per file, per the roadmap convention in the grimoire
(software/conventions/roadmap-convention.md). You may add items freely: write
the file directly with required `title`, `state: inbox`, and `created:` (today,
YYYY-MM-DD), optional `tags`/`source`/`log`, and a body that is a small, clear
prompt -- the problem or solution to execute, not documentation of it; trust
the code and the day's journal entry for what's discoverable, and point a
`log:` stamp at the specific journal entry when a card leans on hard-won
context. Everything above the first `##` heading is the prompt; supporting
material that isn't the prompt goes in named sections below it (`## why` for
justification, `## background` for a longer description), which are
conventional, never required, and never validated. The filename is the slug of
the title (lowercase ASCII, hyphens; discard every other character); never
overwrite an existing file. Read sibling items for the shape.

Hard rules: never touch `order.yaml` (priority is the operator's judgment, set
at triage); never commit roadmap changes unless directed — the uncommitted diff
is the review queue; never delete items; edits change only the lines that
express them. Label the kind from the house set when one fits: defect,
documentation, enhancement, epic, feature, story; add `spike` alongside it when
the work carries unknowns that need discovery.
