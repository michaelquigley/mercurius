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

## Round Lifecycle

1. Validate the session and budget.
2. Snapshot artifacts into `<session_dir>/snapshots/round-NN/`.
3. Build the review prompt with review context, project guidance, artifact contents, prior decisions, decisions log, finding budget, and schema.
4. Dispatch to the selected reviewer.
5. Validate the raw reviewer output against the JSON schema.
6. Validate readiness consistency for `ready_to_ship`, `verdict`, `concerns`, and `questions`.
7. Write `<session_dir>/round-NN.md`.
8. Mark the round completed and make it collectible.

Failures after snapshotting are atomic. Mercurius deletes the partial snapshot directory, does not write a round log, does not advance the round counter, and does not consume budget.

## Prompt Ownership

The broker is the only owner of the prompt. Reviewers receive an assembled `ReviewRequest` with:

- `Prompt`
- `Artifacts`
- `Schema`
- `SessionMeta`

The current prompt always includes these sections in order:

- Role and readiness frame.
- Review context.
- Review criteria.
- Project-specific guidance.
- Artifacts under review.
- Prior decisions and rendered decisions log.
- Verdict and severity definitions.
- Finding budget.
- Output instruction and JSON schema.

Artifact contents are inlined inside dynamic backtick fences so markdown artifacts containing code fences do not corrupt the prompt.

## Session Directory Layout

```text
<log_destination>/
  <session_id>/
    status.json
    events.ndjson
    decisions.md
    round-01.md
    round-02.md
    snapshots/
      round-01/
        design
        work-order
      round-02/
        design
        work-order
```

`status.json` is the latest durable monitor snapshot. `events.ndjson` is append-only lifecycle history. `decisions.md` is generated from recorded round decisions and passed into future reviewer prompts.

## Round Logs

Each completed round writes a markdown log:

- YAML frontmatter with session id, round number, opened timestamp, verdict, reviewer names, and `notes_recorded`.
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
