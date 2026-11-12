---
title: session rehydration
state: evaluating
created: 2026-08-18
tags: [feature]
milestone: v0.1.x
log:
  - stamp: 2026-08-18
    note: spec drawn — docs/future/session-rehydration.md
  - stamp: 2026-11-10
    note: spec redrawn as docs/future/sessions-on-disk.md, replacing the rehydration spec — docs/journal/2026-11-10.md
  - stamp: 2026-11-12
    note: built in three stages on the sessions-on-disk branch; spec and work order retired — docs/journal/2026-11-12.md
---

move the session onto disk so the mcp server holds nothing between tool calls: no in-memory registry, a detached runner process per round that outlives the server, a project-wide runner lock that refuses a second launch and says why, failed rounds kept on disk, the reviewer and `max_findings` read per round, and the reviewer chosen once per installation in `~/.config/mercurius/config.yaml`. built; `docs/current/` describes it, and what was deliberately left out is in `docs/future/sessions-on-disk-followups.md`.

## why

born 2026-08-18 from the scry detail-view arc: pi-mcp-adapter's ten-minute idle shutdown killed the stdio server during triage in four consecutive sessions, and the in-memory tools answered `not_found` for sessions whose full record sat intact on disk. a stdio server's lifetime is whatever the harness adapter gives it; mercurius should not depend on a lifetime it does not control.
