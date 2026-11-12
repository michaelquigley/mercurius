# Sessions on Disk Follow-Ups

Sessions moved onto disk in November 2026: the server holds nothing between tool calls, each round runs in its own detached runner process under a project-wide lock, failed rounds stay on disk, and the reviewer is set per installation. The behavior as built is documented in `docs/current/` (architecture, operations, configuration, the MCP tools). Several things were considered during that work and deliberately left out. They are recorded here so the trigger to revisit each is not lost. Nothing below is implemented.

## A `cancel_round` tool

There is no way to stop a running round through the tool surface. The round timeout bounds a runaway review, and the runner and reviewer pids are recorded in the round's `_state.json` and named in a launch `conflict`, so an operator can kill a round by hand.

The trigger to revisit: killing rounds by hand becoming a habit, or a real want for an agent to cancel one.

## Concurrent rounds in one project

One round runs at a time per log destination. The lock is project-wide on purpose, because a per-session guard misses the path duplicates actually take, which is a new session. Separate worktrees resolve separate log destinations and so get separate locks.

The trigger to revisit: a demonstrated need for two rounds at once in one tree. The lock would move into the round.

## Resuming a round whose runner died

A round whose runner dies is recorded as failed with the reason `runner_lost`, and the retry starts from zero. Whatever the reviewer produced is kept for diagnosis, not for continuation.

## A record of rescued rounds across arcs

A round that finished after its server died is marked `outlived_server`, and the synopsis counts those for one session. Nothing tallies them across sessions or projects, because session directories are deleted when an arc closes and a durable tally would need a home outside the project, which mercurius does not have.

The trigger to revisit: wanting to know how often the detached runner earns its keep, beyond what a single synopsis shows.

## Guarding against a reviewer switch mid-arc

Configuration is resolved per round. An edit to the user-level config, or a `reviewer_override` added or removed, takes effect on the next round. Each round records its reviewer and where it came from, so a switch is visible in the synopsis. Nothing prevents it.

## Reading the user-level config only where it is needed

Both config files are read on every tool call and by the monitor. An invalid user-level config therefore fails every call, including `collect_round` and `record_round_notes`, which never use a reviewer. This was kept on purpose when it was noticed: the error names the file, the fix takes seconds, and a broken installation file is heard about at once.

The trigger to revisit: a half-edited user-level config getting in the way of triage in practice. The narrower rule is to read it only in `open_session` and `start_review_round`.

## Other user-level settings

Only the reviewer block moved to the user-level config. Anything else waits until it shows the same shape: set per installation, and wrong when left to whoever scaffolds the project file.

## Platforms beyond Linux

The release build is Linux-only. The runner lock uses `flock(2)` and works on any unix. The reviewer's parent-death signal and the process-group kill on timeout are Linux arrangements and do nothing elsewhere, so on another platform a reviewer can outlive a dead runner. The documented user-level path is the Linux one; Go resolves a different directory on macOS.

The trigger to revisit: a build for another platform.

## Related

- `docs/current/operations.md` — the residuals the shipped design leaves open on purpose.
- `docs/future/roadmap/bind-project-data-through-dd.md` — moving the on-disk files, the MCP payloads, and the review-output parse onto `dd`.
- `fidelity-gate.md` — leans on the standing refusal to keep a queryable corpus of past sessions, which this work left intact.
- `web-monitor-and-trajectory.md` — reads the on-disk session and round files as its substrate.
