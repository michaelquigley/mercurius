# Mercurius

Workflow broker for design-agent review loops. Mercurius runs as a local MCP server, snapshots design artifacts, dispatches them to a configured reviewer, validates structured review output, and writes durable round logs.

## project context

See `docs/current/` for documentation that describes the current implementation. Start with `docs/current/README.md` for the current docs index and `docs/current/user-guide.md` for real-world usage.

See `docs/future/` for deferred designs, speculative changes, and notes about behavior that is not implemented yet. Do not document future behavior in `docs/current/` until the code exists and tests cover the behavior.

M1-M4 established the Go scaffold, reviewer interface, structured review schema, Codex subprocess reviewer, broker/session orchestration, prompt assembly, snapshotting, log writing, dummy reviewer test support, configuration loading, MCP stdio tool surface, background review rounds, CLI monitoring, review context, and advisory notes. The model was later simplified to single-shot rounds grouped under a lightweight session container; decisions log carry-forward, the convergence signal, the per-session budget, and the close_session verdict were removed in that pass. Sessions then moved onto disk: the server holds no session state between tool calls, reads `mercurius.yaml` and the user-level config per call rather than at startup, and records each round's inputs, state, and outcome as machine files in the round directory; one round runs at a time per log destination under a kernel lock. Each round runs in its own detached runner process (`mercurius run-round`), which outlives the server, holds the lock, and takes its reviewer down with it when it dies. The reviewer is set once per installation in the user-level config (`~/.config/mercurius/config.yaml`); a project overrides it only through `reviewer_override`, and a plain `reviewer` block in `mercurius.yaml` is an error. Project data is then bound through `dd` throughout: machine files are read strictly, tool arguments are checked against hand-written input schemas and bound strictly, and an unknown key in either config file is an error.

## tech stack

- **language**: Go 1.26+
- **CLI**: github.com/spf13/cobra
- **binding**: github.com/michaelquigley/df/dd, for all project data: config, the on-disk session and round files, the MCP tool payloads, and the review-output parse
- **logging**: github.com/michaelquigley/df/dl
- **MCP**: github.com/modelcontextprotocol/go-sdk/mcp
- **schema validation**: github.com/santhosh-tekuri/jsonschema/v6
- **build/test**: standard `go build`, `go vet`, and `go test`

## package structure

- `cmd/mercurius/` - binary entrypoint
- `internal/broker/` - the six session operations as functions over the session directory; holds no state between calls
- `internal/bootstrap/` - the embedded starter templates for the project and user-level config files
- `internal/config/` - resolution of the project `mercurius.yaml` and the user-level config, path resolution, and validation
- `internal/mcpserver/` - MCP server construction, the `dd` binding adapter and the hand-written tool input schemas, tool registration, and error mapping
- `internal/prompt/` - standard review prompt assembly
- `internal/reviewer/` - Reviewer interface, shared request/response types, and the subprocess helper the reviewers share
- `internal/reviewer/claude/` - Claude Code subprocess reviewer implementation
- `internal/reviewer/codex/` - Codex subprocess reviewer implementation
- `internal/reviewer/dummy/` - in-process reviewer for tests
- `internal/reviewer/jsonout/` - JSON-object extraction shared by the reviewers
- `internal/reviewer/pi/` - pi subprocess reviewer implementation
- `internal/roundlog/` - markdown round log, notes, and session synopsis writers
- `internal/runlock/` - the project-wide runner lock (`flock(2)` on `runner.lock`) and its note
- `internal/runner/` - round execution from a round directory, the `Launcher` seam, the process launcher (production) and in-process launcher (tests), and reviewer construction
- `internal/store/` - on-disk session and round file shapes, atomic writes, paths, and the session id check
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

7. a struct carries a `dd` tag only where `dd`'s default would be wrong: `+required` on a tool argument that must be present, `+extra` on a config struct's catch-all, `+omitempty` where a zero value would state something false or read as a setting, `+nullable` on a field that is only ever bound and may arrive as null, `+opaque` on a raw value carried through untouched, or a name override. nothing that is written out carries `+nullable`. `dd` converts CamelCase fields to `snake_case` keys automatically, and project-owned structs carry no `json` tags.

## Roadmap

This repo's roadmap lives in `docs/future/roadmap/` — one frontmatter-markdown item per file, per the roadmap convention in the grimoire (software/conventions/roadmap-convention.md). You may add items freely: write the file directly with required `title`, `state: inbox`, and `created:` (today, YYYY-MM-DD), optional `tags`/`source`/`log`, and a body that is a small, clear prompt -- the problem or solution to execute, not documentation of it; trust the code and the day's journal entry for what's discoverable, and point a `log:` stamp at the specific journal entry when a card leans on hard-won context. Everything above the first `##` heading is the prompt; supporting material that isn't the prompt goes in named sections below it (`## why` for justification, `## background` for a longer description), which are conventional, never required, and never validated. The filename is the slug of the title (lowercase ASCII, hyphens; discard every other character); never overwrite an existing file. Read sibling items for the shape.

Hard rules: never touch `order.yaml` (priority is the operator's judgment, set at triage); never commit roadmap changes unless directed — the uncommitted diff is the review queue; never delete items; edits change only the lines that express them. Label the kind from the house set when one fits: defect, documentation, enhancement, epic, feature, story; add `spike` alongside it when the work carries unknowns that need discovery.

## Project memory

Durable knowledge about this project lives in `docs/journal/`, dated files `docs/journal/YYYY-MM-DD.md`. This is project memory; it does not go in harness-local storage (`.claude/` or equivalent), where it's invisible to every other harness and collaborator and dies with the host. Concretely: do not write to your harness's memory directory or memory tool for this project — even when the harness presents it as the default place for durable knowledge. That tool is the silo this convention exists to replace; the journal is the only durable home.

On arrival, read the most recent entries to pick up where the last session left off, before you start changing things. Treat them as prior-session context, not verified truth — if an entry conflicts with the code or a `docs/current/` doc, the code wins.

Write the smallest entry that carries the session's durable insight, and nothing more. The test for every line: *would a competent agent get this wrong, or waste time rediscovering it, working from the tree alone?* If it's recoverable by reading the code, the diff, `docs/current/`, or git history, leave it out.

That filter keeps four kinds of thing and discards the rest:

- **Decisions whose rationale isn't visible in the result** — why a value was chosen, what a line guards against, why something that looks like dead code or a no-op is load-bearing.
- **Deliberate non-actions** — a change you considered and chose not to make, so the next agent doesn't "fix" it. An unchanged file leaves no trace in a diff.
- **Couplings that span files** — two places that must move together, an ordering that matters, an assumption one file makes about another.
- **Live state** — what's unverified, unfinished, or waiting on something external.

Skip change inventories, restatements of the diff, and play-by-play of how you worked. There's no write-time approval gate; Michael reviews on commit. Append to the day's file if it exists, and write the few lines you'd want the next agent to read — honest and self-contained.

## Commits

The operator commits; agents don't. Never run `git commit` or `git push` in this repo. Finish the edit, leave the change in the working tree (staged is fine), report what changed, and hand off — the uncommitted diff is the review queue and the commit is the operator's act of acceptance. Approval of a change is not direction to commit; only an explicit instruction to commit is, and only for that commit.
