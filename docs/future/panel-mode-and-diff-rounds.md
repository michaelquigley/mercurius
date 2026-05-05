# Panel Mode and Diff Rounds

Panel mode and diff rounds were deferred from the initial implementation. The current code is shaped to make them additive, but it still enforces one reviewer per session and has no diff-round tool input.

## Panel Mode

Panel mode would allow a session to select multiple reviewers for each round.

Expected shape:

- `open_session.reviewers` may name more than one configured reviewer.
- Broker dispatches reviewers in parallel.
- Each reviewer returns an independent output matching the review schema.
- `collect_round.reviewers` contains all reviewer outputs distinctly.
- Round logs contain one reviewer subsection per reviewer.
- Mercurius does not merge findings automatically. The design agent and human make the judgment call.

Main reason to add this: cross-reviewer disagreement is useful signal for high-stakes work. Findings that multiple reviewers independently raise are likely blockers. Findings raised by one reviewer only may be judgment calls or advisory notes.

Open design questions:

- How to budget findings across reviewers.
- Whether convergence should consider reviewer agreement.
- Whether panels should be configured per project or per session.
- Whether mixed model families are worth the added cost.

## Diff Rounds

A diff round would compare current artifacts against the original session artifacts and ask what intent was lost.

Expected shape:

- `start_review_round` or a new tool accepts a diff-round mode.
- The prompt includes original snapshots and current snapshots.
- The reviewer answers a narrower question: what changed in a way that loses original intent?
- Output can reuse the current schema.
- Round logs identify the round as a diff round.

Main reason to add this: long review loops can converge toward the reviewer or design agent's preferences while drifting away from the human's original intent.

Open design questions:

- Whether round 1 or session-open snapshots are the correct baseline.
- Whether diff rounds consume the normal session budget.
- Whether diff rounds should be manually requested only or suggested by convergence heuristics.
