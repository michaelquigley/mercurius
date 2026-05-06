# Web monitor and trajectory analytics

The current `mercurius monitor` CLI tail-prints session status, lifecycle events, and the current next-action. It works, but the shape is wrong for what's actually happening in a review session: a sequence of rounds with structured findings, accumulating decisions, severity composition that decays over time, and side files (snapshots, prompt logs, decisions log) the operator wants to navigate.

The trajectory framing from `mercurius-feedback.md` (proposal #7, "richer convergence signal") is the same problem viewed from a different angle. The valuable signal is not "is this round done?" but "how is the severity character of findings shifting across rounds?" That's hard to encode in a JSON `convergence` field and unreadable in a CLI tail. It's easy to read in a chart.

This document sketches the direction; a future design session will produce the spec.

## Vision

A local web application that gives the operator a session-aware, trajectory-aware view of what mercurius is doing.

- Augments rather than replaces the CLI monitor. `mercurius monitor` stays useful in scripts and `--wait` flows.
- Long-running but stateless. All state lives in the broker's existing on-disk artifacts.
- Multi-session and multi-project capable. Point it at a parent directory; it discovers `.mercurius/` trees underneath.

## Architecture (proposed)

File-watch only. No new RPC surface, no daemon protocol with the broker.

The broker already writes everything important to disk:

- `<session_dir>/status.json` — latest session/round state
- `<session_dir>/events.ndjson` — append-only event stream
- `<session_dir>/round-NN.md` — round logs with frontmatter, findings, decisions
- `<session_dir>/decisions.md` — accumulated decisions log
- `<session_dir>/snapshots/round-NN/` — artifact snapshots and `_prompt.md`

The web app watches `.mercurius/` directories under a configured root, parses on-disk state, serves a local HTTP UI (port configurable, localhost-bound by default), and pushes updates via SSE or websocket as files change. Brokers stay unchanged. No new protocol to design or version. The web app can be killed and restarted without disturbing in-flight sessions.

## What it surfaces

- **Session list**: all known sessions across configured roots, with status, round count, latest verdict.
- **Session detail**: round timeline, per-round findings and advisories, decisions log, links into snapshots and prompt logs.
- **Round detail**: findings table sorted by severity, advisory list, dispositions, artifact diff between snapshots, prompt log viewer.
- **Trajectory analytics panel**: severity composition over rounds, domain shift, recurrence rate, advisory-to-blocker ratio.
- **Cross-session views** (later): comparing fresh-start vs continued sessions on the same artifacts.

## Trajectory analytics

The "richer convergence signal" proposal from the feedback doc becomes concrete in this UI:

- **Severity composition per round.** Counts of blocker / major / minor over rounds. The decay curve is the readiness signal.
- **Domain shift.** Each finding gets a domain tag (architectural / spec / polish / wording). Tags are LLM-inferable from finding location and title at collect time, or applied manually.
- **Recurrence detection.** Findings whose `claim` or `topic` matches a prior round's finding (similarity over text). Surfaces "we keep raising this even though it's been adjudicated."
- **Advisory ratio.** As a session converges, advisory_notes share rises relative to blocking findings.

These run in the web server with no broker changes. They read the same JSON the operator already sees; the value-add is presentation and accumulation across rounds.

## Open design questions for the future session

- Single project vs multi-project root: one `mercurius.yaml` like the broker, or root-discovery across many projects?
- Auth model: localhost-only is safe by default; should there be additional access controls?
- Trajectory tagging (domain): manual, LLM-inferred at collect time, or both?
- Stateful trajectory analytics: persist computed similarity across runs, or recompute on every load?
- Whether the web app subsumes the JSON `convergence` field in `collect_round` or runs alongside it.
- Tech stack: Go HTTP server with embedded assets vs separate frontend repo. Embedded keeps mercurius a single binary.

## Why this is its own arc

The web app is real infrastructure (long-running process, web frontend, asset pipeline) separable from the protocol-shape changes mercurius is otherwise iterating on. Folding it into a smaller bundle would obscure both. Splitting keeps the smaller bundles tight and gives the web monitor the design attention it needs.

Pickup order: after the review-loop ergonomics bundle (review_focus override, advisory refs, fixed disposition, next_finding by severity, prompt preview, docs reframes) lands. None of those depend on this; this depends on none of those.
