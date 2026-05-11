# Architecture and Storage

## Components

```mermaid
flowchart LR
    Human([Human])
    DA[Design Agent]
    M{{Mercurius MCP server}}
    B[Broker]
    R[Reviewer]
    FS[(Session directory)]

    Human <--> DA
    DA -- MCP tools --> M
    M --> B
    B --> R
    B --> FS
    R --> B
    B --> M
    M --> DA
```

The broker owns session state, prompt assembly, artifact snapshots, reviewer dispatch, validation, and log writing. Reviewer implementations only run an underlying reviewer and return raw structured output.

## Session and Round Model

A **session** is a light grouping of related rounds. It owns an id, a folder, a selected reviewer, and an open/closed lifecycle. It does not own artifacts, decisions, or any state shared between rounds.

A **round** is the atomic review unit. It owns its artifacts, snapshot, prompt, reviewer output, and optional commentary/decisions. Rounds are single-shot: one round dispatches one review and accepts notes/decisions exactly once.

Nothing flows between rounds within a session: no decisions log carry-forward, no prior-decisions prompt block, no budget tracking, no convergence signal. To iterate, run another round in the same session with updated artifacts (rounds-as-snapshots history), or close the session and open a new one (fresh-eyes pattern). Both are valid - many users find the fresh-session loop more effective than multi-round-within-session arcs.

## Round Lifecycle

1. Validate session and artifacts.
2. Snapshot artifacts into `<session_dir>/round-NN/`.
3. Build the review prompt with review context, what-to-flag criteria, fix sizing, project-specific focus, artifact contents, verdict/severity definitions, finding budget, and output schema. The prompt has no prior-decisions or decisions-log sections.
4. Write the assembled prompt to `<session_dir>/round-NN/_prompt.md`.
5. Dispatch to the selected reviewer.
6. Validate the raw reviewer output against the JSON schema.
7. Validate readiness consistency between `verdict`, `concerns`, and `questions`.
8. Write `<session_dir>/round-NN/_round.md`.
9. Mark the round completed and make it collectible.

Failures after snapshotting are atomic. Mercurius deletes the partial round directory, does not write a round log, and does not advance the round counter.

## Prompt Ownership

The broker is the only owner of the prompt. Reviewers receive an assembled `ReviewRequest` with:

- `Prompt`
- `Artifacts`
- `Schema`
- `SessionMeta` (session id + round number)

The current prompt always includes these sections in order:

- Role and readiness frame.
- Review context.
- What to flag.
- Fix sizing.
- Project-specific focus.
- Artifacts under review.
- Verdict and severity definitions.
- Finding budget.
- Output instruction and JSON schema.

Artifact contents are inlined inside dynamic backtick fences so markdown artifacts containing code fences do not corrupt the prompt.

The assembled prompt is also written to `<session_dir>/round-NN/_prompt.md` during the snapshot step. The leading underscore reserves a namespace for broker-emitted meta files inside the round directory; artifact names cannot begin with `_`. The round log frontmatter includes a `prompt_path` field pointing at this file.

## Session Directory Layout

```text
<log_destination>/
  <session_id>/
    status.json
    events.ndjson
    round-01/
      _round.md
      _prompt.md
      design
      work-order
    round-02/
      _round.md
      _prompt.md
      design
```

Each round gets its own self-contained folder. `_round.md` is the log file (immutable review content plus a mutable notes region). `_prompt.md` is the assembled prompt. Other files in the directory are the snapshotted artifacts under their declared names.

`status.json` is the latest durable monitor snapshot of session state. `events.ndjson` is append-only lifecycle history.

There is no session-level `decisions.md`: decisions live in the round log they pertain to, and do not carry forward.

## Round Logs

Each completed round writes a markdown log at `<session>/round-NN/_round.md`:

- YAML frontmatter with session id, round number, opened timestamp, verdict, prompt path, reviewer names, and `notes_recorded`.
- Artifact manifest table.
- Reviewer output sections with usage notes and raw JSON.
- Mutable commentary and decisions region bounded by `<!-- mercurius:notes-begin -->` and `<!-- mercurius:notes-end -->`.

Only the mutable notes region and `notes_recorded` frontmatter field are rewritten by `record_round_notes`. Artifact manifests and reviewer outputs are immutable after the log is created.

## Reviewer Implementations

Current implementations:

- `codex`: runs the local `codex` CLI as a subprocess.
- `dummy`: returns a fixed valid response for tests and scaffolding.

The Codex reviewer runs `codex exec` with:

- working directory set to the session directory
- ephemeral session
- read-only sandbox
- JSON schema file supplied through `--output-schema`
- final message captured through `--output-last-message`
- assembled prompt delivered on stdin

The reviewer performs output extraction only. Schema validation belongs to the broker.
