# Bounding Reviewer Error Output in status.json

When a review round fails because the reviewer's output cannot be parsed or validated, the broker records the failure on the session's `last_error` and persists it into `status.json`, embedding the raw reviewer output inline at `last_error.details.raw`. For reviewers that emit a large event stream — notably `pi`, whose stdout is the full agent transcript — that raw payload can be several megabytes. A smoke-test round with a local `pi` model left an 8 MB `status.json` from a single failed round.

This note describes the wrinkle and a deferred fix. No cap is implemented today; the raw output is persisted in full.

## Why it matters

`status.json` is the durable monitor snapshot. `mercurius monitor` reads it on every poll, and the proposed file-watch web app (`web-monitor-and-trajectory.md`) is built entirely around cheaply re-reading and parsing it as files change. A multi-megabyte `status.json` produced by one failed round slows every monitor poll and would force the web app to re-parse a large blob on each change event — the opposite of the lightweight file-watch the web monitor assumes.

The bloat is specific to the failure path. On a successful round the raw stream is parsed down to the structured review object, which lands in `round-NN/_round.md`; `status.json` stays small. Only `last_error.details.raw` carries the unbounded payload, and only when a round fails. (The same `pi` run emitted roughly 27 MB of stdout on a successful round, all of it discarded after parsing.)

## Direction

Cap the persisted raw output. The smallest-shaped fix is to truncate `last_error.details.raw` to a bounded size — for example the head and tail of the stream with an elision marker between — keeping enough to diagnose the failure without storing the whole transcript. The `cause` field already carries the short, actionable reason (exit status, stderr, or schema-violation message); `raw` only needs to be a bounded diagnostic sample, not a complete record.

If full-fidelity capture is ever wanted, it belongs in its own file rather than inline in `status.json`, following the pattern `_prompt.md` and `_config.yaml` already set. Note that a failed round is discarded atomically — its `round-NN/` directory is removed — so a full-raw sidecar would need a session-level location rather than the (now absent) round directory.
