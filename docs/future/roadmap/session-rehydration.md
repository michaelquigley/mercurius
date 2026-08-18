---
title: session rehydration
state: researching
created: 2026-08-18
tags: [feature]
milestone: v0.1.x
log:
  - stamp: 2026-08-18
    note: spec drawn — docs/future/session-rehydration.md
---

make the broker's session registry survive process restarts: rehydrate a session lazily on registry miss from its on-disk record, write machine twins (`_review.json`, `_notes.json`) beside the human round documents, gate attachment with an owner-pid sentinel (takeover on death only), and add a `--no-rehydration` per-harness opt-out. the spec is `docs/future/session-rehydration.md`; work from it.

## why

born 2026-08-18 from the scry detail-view arc: pi-mcp-adapter's ten-minute idle shutdown killed the stdio server during triage in four consecutive sessions, and the in-memory tools answered `not_found` for sessions whose full record sat intact on disk. a stdio server's lifetime is whatever the harness adapter gives it; mercurius should not depend on a lifetime it does not control.
