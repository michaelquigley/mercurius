# Architecture and Storage

## Components

```mermaid
flowchart LR
    Human([Human])
    DA[Design Agent]
    subgraph S[Server process]
        M{{Mercurius MCP server}}
        B[Broker]
    end
    RN[Runner process]
    R[Reviewer process]
    FS[(Session directory)]
    MON[Monitor]

    Human <--> DA
    DA -- MCP tools --> M
    M --> B
    B -- reads and writes --> FS
    B -- starts, detached --> RN
    RN -- runs --> R
    RN -- writes the round --> FS
    MON -- reads --> FS
```

The session directory is the only session state Mercurius has. The server holds nothing between tool calls: every tool call resolves the configuration, finds the session directory, reads what it needs, acts, and writes. Any server process can serve any session, so a session survives the server that opened it, and a session opened under one harness can be triaged and closed under another.

The broker owns configuration resolution, prompt assembly, artifact snapshots, and the round's inputs. The round runner owns reviewer dispatch, validation, and the round's outcome. Reviewer implementations only run an underlying reviewer and return raw structured output.

There are three kinds of process, and each lives exactly as long as its job:

- The **server** is the MCP surface. It lives as long as the harness allows and holds only where its configuration lives.
- The **runner** is one round. `start_review_round` starts it as a separate, detached mercurius process, and it lives until the round reaches an outcome, whether or not the server that started it is still there.
- The **reviewer** is the runner's child: `codex`, `claude`, or `pi`.

There is no protocol between the server and the runner; the round directory is the whole interface. See The Runner Process.

## Session and Round Model

A **session** is a light grouping of related rounds. It owns an id, a folder, and an open/closed lifecycle. It does not own artifacts, decisions, a reviewer, or any state shared between rounds.

A **round** is the atomic review unit. It owns its artifacts, snapshot, prompt, the reviewer and finding limit it ran with, its calibration, its reviewer output, and optional commentary/decisions. Rounds are single-shot: one round dispatches one review.

Nothing flows between rounds within a session: no decisions log carry-forward, no prior-decisions prompt block. To iterate, run another round in the same session with updated artifacts, or close the session and open a new one. Both are valid.

Configuration is per round. The reviewer (from the user-level config, or the project's `reviewer_override`), `max_findings`, `review_context`, `review_focus`, and `settled_decisions` are resolved when a round starts and recorded in that round's `_run.json`, with the reviewer's source in `reviewer_source`, so an edit to any of them takes effect on the next round, and every round's record says what it actually ran with.

## Round Lifecycle

`start_review_round` does its checking up front, and anything that fails before the round is handed to its runner is returned directly from the tool call and leaves no round behind.

1. Resolve `mercurius.yaml` and the user-level config. A missing or unparseable project file, a `reviewer` block in it, an invalid user-level file, or no reviewer in either is a `user_error`.
2. Check the session id, read `session.json`, and refuse a closed session.
3. Validate the artifacts.
4. Take the project's runner lock. If it is held, refuse with `conflict` naming the running round.
5. If the round named in the lock file's note is still marked running, record it failed with reason `runner_lost`.
6. Remove any round directory in this session with no `_state.json` (a leftover from a server that died while preparing it), and number the new round one more than the highest round directory left.
7. Write this round's note into the lock file.
8. Create `round-NN/`: snapshot the artifacts, write `_prompt.md` (the assembled prompt), `_config.yaml` (the exact project config bytes), and `_run.json` (the round's inputs).
9. Write `_state.json` as `running`. The round exists from this moment.
10. Start the runner process, passing it the lock, close the server's own copy of the lock, and return.

The runner then reads `_run.json` and `_prompt.md`, builds the reviewer from the reviewer block recorded in `_run.json`, dispatches it under the round timeout, validates the raw output against the JSON schema, readiness consistency, and the finding limit, and writes the outcome:

- on success, `_review.json` and `_round.md`, then `_state.json` as `completed`;
- on failure, whatever the reviewer produced to `_raw.txt`, then `_state.json` as `failed` with the stored error.

`_state.json` is always written last, so a round that says `completed` has its outcome on disk. Along the way the runner records its own pid (`runner_pid`) and, once the reviewer has started, the reviewer's (`reviewer_pid`) in `_state.json`, for the operator to read. At finish it records `outlived_server`: true when the server that prepared the round, named by `server_pid` in `_run.json`, is no longer the runner's parent. A round that outlived its server is one the old in-memory design would have lost. The runner releases the lock when it exits.

### The round timeout

Every round is bounded by `reviewer.timeout` (default `20m`). When the deadline passes, the runner kills the reviewer's whole process group, so the reviewer's own children die with it. If the reviewer had already produced a valid review, the round completes with it: a finished review is not thrown away because its process failed to exit. Otherwise the round fails with `reviewer_failed` and `details.reason: timeout`. The default errs long on purpose; a timeout that fires early throws away a good review, while one that fires late only holds the lock a while longer.

### Failed rounds stay

A round that was started keeps its directory and its number, whatever happened to it. Round 2 fails, stays on disk as failed, and the retry is round 3. The failure is recorded in `_state.json`; the reviewer's output, if any, is in `_raw.txt` beside it. `collect_round` on a failed round returns that failure, from disk, for as long as the directory exists. The synopsis lists failed rounds with the rest.

The stored error is kept small. `details.cause` is cut to 8 KiB with a marker saying so, and raw reviewer output is never stored in an error; `details.raw_path` points at `_raw.txt` instead.

## The Runner Lock

One round runs at a time per log destination. The lock is a kernel `flock(2)` on `<log_destination>/runner.lock`, held for the round's whole life and released by the kernel when its holder closes it or exits, however it exits. Mercurius does no pid probing and writes no heartbeats.

The lock answers two questions:

- **Is a round running?** If the lock is held, yes. `start_review_round` refuses with `conflict`, built from the lock file's note and the named round's state: which session and round is running, when it started, how long it has been running, the monitor command for that session, and a `next_action` saying to wait and then `collect_round` on that session. The round is not lost, so the right move is to collect it rather than relaunch it.
- **Is this round dead?** A round whose state says `running` while the lock is free has no runner. When `collect_round`, `session_status`, or `close_session` finds a session's latest round running, it makes one attempt at the lock; if it gets it, it re-reads the round's state while holding the lock and, if the state still says running, records the round failed with `reviewer_failed` and `details.reason: runner_lost`. The re-read matters: a runner writes its final state and then lets go, so a state read before the lock was taken can be stale.

The lock is therefore also the permission to write a running round's state; nothing touches `_state.json` without holding it. The monitor makes the same attempt read-only and never writes: it prints such a round as lost and leaves it for the next server call.

The guard is project-wide rather than per session on purpose. The path duplicate launches actually take is a new session: an agent loses the session id, or gets an error, opens a fresh session, and launches there. A per-session guard never sees that. The cost is that two harnesses cannot run reviews at the same moment in one project; separate worktrees each resolve their own log destination and so get their own lock.

The lock file's content is a note about the current holder (`session_id`, `round_number`, `server_pid`, `started_at`). The note is rewritten in place through the held descriptor; the file is never replaced, because the lock belongs to the file's inode. The launch `conflict` reports `started_by_this_server` by comparing the note's `server_pid` with the answering server's own, and `runner_pid` from the running round's state.

The server takes the lock and hands it to the runner. The lock belongs to the open file description, so a child that inherits the descriptor shares it, and it is released when the last copy closes. Two rules keep exactly one holder:

- **The server closes its copy after the handoff.** If it kept it, the lock would stay held for as long as the server lived, and every later launch in the project would be refused.
- **The runner marks the inherited descriptor close-on-exec before it starts anything.** A descriptor passed to a child arrives without that flag; left clear, the reviewer would inherit the lock and hold it past the runner's death.

## The Runner Process

The server starts each round's runner as `mercurius run-round --round-dir <dir>`, a hidden subcommand of the same executable. The runner gets:

- its own session and process group, so a harness taking down the server, or the server's process group, or sending it a hangup, does not take the round with it;
- the null device on stdin, and `_runner.log` in the round directory for stdout and stderr, since a detached process has no terminal anyone will read;
- the runner lock as its one inherited descriptor.

The server waits on the runner in the background, so a runner that finishes while its server is alive is reaped rather than left a zombie. A runner whose server is gone is adopted and reaped by the system.

The reviewer's life is tied to the runner's by the kernel. Each subprocess reviewer is started with a parent-death signal (`SIGKILL`) and as the leader of its own process group. When a runner dies, however it dies, its reviewer dies with it. The parent-death signal follows the thread that started the child, not the process, so the runner locks its goroutine to its OS thread for the whole round and runs the reviewer synchronously on it; otherwise the Go runtime could retire that thread and the kernel would kill a healthy reviewer. These are Linux arrangements; elsewhere the reviewer is started without them.

Mercurius never signals a process by a pid it read from disk. The pids in `_state.json` are for the operator: to see what is running, or to kill a round deliberately. Pids get reused, and a round found dead days later would be hunting a number that now belongs to something else. The timeout's kill names the process group of the reviewer the runner itself just started, never a recorded pid.

## Prompt Ownership

The broker is the only owner of the prompt. Reviewers receive an assembled `ReviewRequest` with:

- `Prompt`
- `Artifacts`
- `Schema`
- `SessionMeta` (session id + round number)
- `Started` (called with the reviewer subprocess's pid once it starts, which the runner records as `reviewer_pid`)

The current prompt always includes these sections in order:

- Role and readiness frame.
- Review context.
- Settled decisions (only when guards are configured).
- What to flag.
- Fix sizing.
- Project-specific focus.
- Artifacts under review.
- Verdict and severity definitions.
- Finding budget.
- Output instruction and JSON schema.

Artifact contents are inlined inside dynamic backtick fences so markdown artifacts containing code fences do not corrupt the prompt.

The assembled prompt is written to `<session_dir>/round-NN/_prompt.md` when the round is prepared, and the runner reads it back from there, so the prompt the reviewer received and the prompt on disk are the same bytes. The leading underscore reserves a namespace for broker-emitted meta files inside the round directory; artifact names cannot begin with `_`. The round log frontmatter includes a `prompt_path` field pointing at this file.

## Session Directory Layout

```text
<log_destination>/
  runner.lock            held by the running round; its content names that round
  <session_id>/
    session.json         the session's own facts: opened, closed
    _synopsis.md         written by close_session
    round-01/
      _state.json        running, completed, or failed; timings; pids; outlived_server; the stored error
      _run.json          the round's inputs: reviewer, max_findings, calibration presence, manifest
      _config.yaml       the project's mercurius.yaml, verbatim
      _prompt.md
      design
      work-order
      _review.json       the outcome, on success
      _round.md
      _raw.txt           the reviewer's raw output, on failure
      _runner.log        the runner's own log
      _notes.json        triage, written by record_round_notes
      _notes.md
```

Each fact has one home, and the homes are divided by who writes them. `session.json` carries what belongs to the session and nothing else: its id, when it opened, and whether and when it closed. A round directory carries everything about that round. The server writes the inputs and the round's first state; the runner writes the outcome and the final state; `record_round_notes` writes the notes later.

The human documents each have a machine twin written in the same motion: `_review.json` beside `_round.md`, `_notes.json` beside `_notes.md`. Mercurius reads the twins and never parses its own markdown back. The refs a round accepts for decisions are not stored; they are recomputed from the raw output in `_review.json` through the same schema parse that produced them. The artifact manifest is an input, known before the reviewer runs and as true of a failed round as of a completed one, so it lives in `_run.json`.

A round exists once its `_state.json` exists. A round directory without one is a leftover from a server that died while preparing it; it is not listed, and the next `start_review_round` in that session removes it.

`_config.yaml` is the exact `mercurius.yaml` bytes this round ran with, captured beside the rendered prompt so the round's input and output sit together: a guard present in `_config.yaml` but absent from `_prompt.md` (rendered empty) stays diagnosable after the fact. Because they record the raw config and the resolved reviewer, `_config.yaml` and `_run.json` can contain a `binary_path`, `model`, or `extra_args`; the `.mercurius/` tree is local and gitignored, so treat it accordingly. Other files in the round directory are the snapshotted artifacts under their declared names.

All JSON files are written through a temporary file renamed over the target, so a reader never sees a partial file. Session ids from tool calls and from `monitor --session` are checked against `^s_[A-Za-z0-9]{12}$` before they are joined into a path; anything else is `not_found`. A matching directory with no `session.json` is `not_found` with a detail saying it is not a session in this layout; sessions written by older Mercurius versions are not read. A `session.json`, `_run.json`, or `_state.json` that exists and cannot be parsed is an `internal_error` naming the file.

## Round Logs

Each completed round writes a markdown log at `<session>/round-NN/_round.md`:

- YAML frontmatter with session id, round number, opened timestamp, verdict, prompt path, and reviewer names.
- Artifact manifest table.
- Reviewer output sections with usage notes and raw JSON.

The log is immutable after creation. Recording commentary and decisions through `record_round_notes` writes `_notes.json` and `_notes.md` rather than mutating the log.

## Session Synopsis

`close_session` writes a session-level summary at `<session>/_synopsis.md`, rendered from the session directory's machine files, so it works from any server process. The file opens with YAML frontmatter (session id, opened/closed timestamps, round count, and the failed-round count when any failed), followed by a skimmable Summary section, a Round outcomes table with one row per round (its state, reviewer, verdict, finding counts, decisions, and notes), and a per-round detail block that restates the round's state, reviewer, calibration presence, verdict, findings, decisions, and commentary. A failed round gets a row and a short detail block with its stored error and the path to `_raw.txt`. A round that outlived its server is marked in its detail block, and the frontmatter counts them (`outlived_server_count`); across an arc, that count is the evidence for how often a server dies under a running round. There is no session-level reviewer; the reviewer is reported per round. If a completed round's reviewer output fails to parse, the synopsis emits a stable fallback pointing at that round's `_review.json`. The per-round files remain the authoritative record.

## Reviewer Implementations

Current implementations:

- `codex`: runs the local `codex` CLI as a subprocess.
- `claude`: runs the Claude Code CLI (`claude`) as a subprocess.
- `pi`: runs the [pi](https://pi.dev) coding agent as a subprocess.
- `dummy`: returns a fixed valid response for tests and scaffolding.

Every reviewer is given the same broker-assembled prompt (which inlines the artifact content and the output schema) and returns the best JSON object it can. Reviewers perform output extraction only; canonical schema validation belongs to the runner, which validates the returned object after the reviewer returns. The shared `internal/reviewer/jsonout` package holds the JSON-object extraction logic used across reviewers.

The three subprocess reviewers share two behaviors:

- **Failures carry their output.** A failed process, or a clean exit whose output cannot be parsed, returns an error carrying the bytes the reviewer captured (stdout and stderr, and for codex the last-message file), instead of folding them into the error message. The runner writes those bytes to `_raw.txt`, so the stored error stays short.
- **The wait is bounded.** Each reviewer's subprocess has a ten-second wait delay: once the reviewer exits or its deadline passes, Mercurius waits at most that long for its output pipes to close before closing them itself. Without it, a process the reviewer started could hold the pipes open and keep the round running indefinitely. A reviewer that wrote a valid result and exited, leaving a child behind, still completes its round. The child is not killed.
- **The reviewer's life is the runner's.** On Linux each reviewer is started with a parent-death signal and as the leader of its own process group; the round timeout kills that group. See The Runner Process.

The Codex reviewer runs `codex exec` with:

- ephemeral session
- read-only sandbox
- JSON schema file supplied through `--output-schema`
- final message captured through `--output-last-message`
- assembled prompt delivered on stdin

The Claude reviewer runs `claude -p` with:

- `--output-format json` and native `--json-schema` enforcement
- `--permission-mode plan` (read-only) and `--no-session-persistence` (ephemeral)
- assembled prompt delivered on stdin
- the schema-validated object read from the envelope's `structured_output` field, falling back to extracting a JSON object from the `result` text

`--bare` is intentionally not passed, so claude inherits the operator's logged-in credentials; a logged-out operator surfaces as a clear reviewer error. Usage notes record the model, subtype, cost, turn count, and duration.

The pi reviewer runs `pi -p --mode json` with:

- `--no-session` (ephemeral) and `--no-context-files` (so the project's `AGENTS.md`/`CLAUDE.md` above the round directory do not colour the review)
- `--tools read,grep,find,ls` (read-only)
- the assembled prompt passed as an `@file` reference, with stdin closed
- the final assistant message parsed out of the JSON event stream, then the JSON object extracted from it

pi has no native JSON-schema enforcement, so the schema is carried only by the prompt and the broker's post-return validation is the backstop.

### Working directory and project context files

Every reviewer runs with its working directory set to the round's snapshot directory (`<session>/round-NN/`), which sits inside the project tree. The reviewers differ in how much of the surrounding project they pull into the review:

- `codex` and `claude` discover and load the project's agent context files (`AGENTS.md` for codex; `CLAUDE.md` for claude, which falls back to `AGENTS.md` when no `CLAUDE.md` is present) by walking up from the working directory, so a review carries whatever instructions those files hold. Codex runs with an isolated `CODEX_HOME`, and claude is not run with `--bare` (so it uses the operator's login); neither suppresses context-file discovery.
- `pi` is run with `--no-context-files`, so it does not load `AGENTS.md`/`CLAUDE.md` and reviews the artifacts on their own merits.

This is a deliberate, tunable difference rather than a guarantee of identical context across reviewers. The artifact content itself is always inlined into the prompt, so context-file discovery only adds the project's own agent instructions, not the artifacts. An operator who wants codex/claude to review without that surrounding context can suppress it per reviewer (for example, run claude with `--bare` plus `ANTHROPIC_API_KEY` via `extra_args`).
