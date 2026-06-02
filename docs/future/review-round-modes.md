# Review Round Modes

A standard review round asks one question: are these artifacts ready to build? Several other questions are worth asking of the same artifacts, each needing a different reviewer posture and a different point of comparison. They run on the same harness — a fresh ephemeral reviewer, structured output, a round log, per-round snapshots — and differ only in what the round compares against and what the reviewer is told to look for.

None of the modes below are implemented. The standard round is the only one that exists today; this note is a catalog of deferred variants.

A round can be thought of as parameterized on two axes:

- **What it compares against** — the current artifacts (standard), an earlier version of them (diff/drift), the artifacts' declared scope or the real code surface (completeness), or the artifacts with guards suppressed (the fresh-reader round, below).
- **What posture it takes** — find real problems in what is there (conservative, the standard reviewer), or find what is absent or has shifted (expansive).

The discipline is the same across all of them: earn each mode on real arcs before building it. The standard, diff/drift, completeness, and fresh-reader rounds are four members of one family that differ only by comparison-target and posture; the worthwhile design, once two or three are concrete, is the *mode* concept itself rather than each mode bolted on as a one-off. Panel mode (below) is orthogonal — it varies the *number* of reviewers, not the question asked, so it composes with any of the others.

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

## Diff / Drift Rounds

A diff round (also called a drift round) compares the current artifacts against an earlier snapshot and asks where the design's *intent* has shifted — not where the text changed, but where the meaning moved without anyone deciding it should. Long review loops can converge toward the reviewer's or design agent's preferences while drifting away from the human's original intent; this is the check against that.

The per-round artifact snapshots make it cheap: the earlier version is already on disk, so a diff round needs no new capture machinery.

Expected shape:

- `start_review_round` or a new tool accepts a diff-round mode.
- The prompt includes the baseline snapshot and the current snapshot.
- The reviewer answers a narrower question: what changed in a way that loses or contradicts original intent?
- Output can reuse the current schema. Round logs identify the round as a diff round.

Sharpenings:

- **Diff against the decisions log, not just the two versions.** A raw before/after diff flags everything, because the artifacts are *supposed* to change across an arc. The signal that matters is drift that *no recorded decision explains* — cross-reference the textual delta against the round notes/decisions and surface only the unexplained changes. That is the dangerous kind: a late edit that quietly contradicted an early decision nobody revisited.
- **Surface for deliberation, not auto-revert.** Divergence from the baseline is a flag to discuss, not an error to correct. Intent is allowed to evolve — sometimes the original was wrong. The round's job is to make the shift visible, not to regress toward the baseline.
- **It is a distinct posture, not code review.** It judges self-consistency across versions — no code needed, just the two snapshots plus the decision trail. It is the temporal twin of the planning agent's pre-handoff self-audit: that pass checks consistency across *sections of one version*; the diff round checks consistency across *versions over time*, with the baseline as a fixed reference the human self-audit never re-reads.

Open design questions:

- Baseline: round 1 snapshot or session-open snapshot? (Round 1 has the first reviewed form; session-open may predate the first real draft.)
- Trigger: manual only, convergence-heuristic suggested, or run once at close before the synopsis is sealed? (Close is attractive — it is the last gate before the arc is sealed, exactly when confirming intent held is most valuable.)
- Whether diff rounds consume the normal finding budget.

## Completeness Rounds (what did we miss?)

A completeness round inverts the standard reviewer. The standard reviewer judges *what is there* — is this content correct, consistent, free of subtle bugs. A completeness round judges *what is absent* — what should be in scope and is not present at all.

This is a genuinely separate mode, not a hotter standard review, because the standard prompt actively *suppresses* completeness: it says prefer fewer findings, and do not flag a missing list entry whose absence is obvious. Those are correct instructions for a readiness reviewer and exactly wrong for a completeness critic, which needs the opposite calibration — expansive, hunting for whole categories that are absent. It is also the hardest thing for the author or design and planning agents to self-check: you cannot see the shape of what you did not think to include — you carry the same blind spots that produced the gap. Fresh context is structurally better positioned, the same reason the standard reviewer reads cold.

Main reason to add this: this class of miss — a whole capability absent, rather than a present thing being wrong — currently survives the entire pipeline (design, planning, mercurius review, implementation) and is discovered only at first run, after the full cost has been paid. A motivating real case: a financial account system focused on reviewing new transactions shipped with no way to view a transaction once it left the new-transaction queue — a state with no observer. Nothing in the artifacts was wrong, so the standard reviewer found nothing; the gap was in the scope boundary itself. A cold completeness pass against the spec could have surfaced it for a fraction of the first-run cost.

The anchor is the whole game. A completeness critic with no anchor is a "have you considered..." firehose — infinite plausible absences, most of them noise the operator already decided against. Give it concrete grounds instead:

- **Lifecycle / round-trip.** For every state an entity can enter and every thing the system persists, is there a path to observe it, retrieve it, reverse it? (The financial miss is exactly this: the `processed` transaction state had no view; data written, never read.) This is the lens the field case pointed at, and it produces a short, structural list — enumerate states, flag the ones with no observer — rather than a brainstorm.
- **Declared scope.** Does the work order actually cover everything the spec claims to cover? (Weaker alone: when the *scope boundary itself* is drawn wrong — as in the financial case — a scope-anchored check ratifies the wrong boundary and misses the gap. Useful, not sufficient.)
- **Code / data surface.** What consumers or integration points of a touched component went unmentioned? What is persisted but never read?
- **Failure modes / journeys.** What happens when an external dependency is down? Does each user journey have an end, not just a start?

Because it over-generates absences by nature, a completeness round needs the shipped `settled_decisions` guards as its suppression floor: *these absences are intentional; find the unintentional ones.* The settled-decisions feature is what makes this mode tractable at all.

Expected shape:

- A round mode that runs one or more anchored passes (lifecycle, dataflow, failure-modes, journeys), each blind to the others — a multi-modal sweep rather than a single "what did we miss" prompt.
- Each pass receives the artifacts plus its anchor framing and the active guards.
- Output can reuse the current schema; findings are absences ("X has no Y") rather than defects.

Open design questions:

- One mode with selectable anchors, or several distinct passes?
- Trigger: late only? Early rounds are legitimately incomplete; "what is still missing" is a meaningful question only near convergence.
- How hard to lean on the code surface versus the artifacts alone — reading the real code catches "written but never read," but pulls the reviewer toward implementation it is meant to precede.

## Fresh-Reader Rounds

A fresh-reader round suppresses the `settled_decisions` guards for one round only, keeping the calibration. It asks whether the artifacts hold up on their own merits rather than under the accumulated permission slips a long arc builds up. The calibration-versus-guards split that shipped with the settled-decisions feature is what makes this cheap: the guards already render as their own prompt block, so dropping them for a single round is a localized change rather than an untangling.

Expected shape:

- `start_review_round` takes a flag (for example `fresh_reader: true`) that omits the settled-decisions block for that round alone.
- Calibration (`review_context`, `review_focus`) is unchanged; only the guards are withheld.
- Output reuses the current schema; the round log records that guards were suppressed, so a finding the guards would normally have silenced is legible as such.

Main reason to add this: guards are, by nature, instructions to *not see* something. Over an arc they accumulate, and a reviewer reading under a thick stack of them is a primed witness rather than a cold reader. A fresh-reader round is the periodic check that the artifacts still survive a reader who was told nothing to ignore — that the guards are suppressing settled noise and not load-bearing problems.

Why it is deferred: guards earn their keep on real arcs, so this is an occasional verification move, not part of the core loop. There is no clean approximation today — because `settled_decisions` is re-read every round, even closing and reopening the session re-applies the guards from the file; getting a guard-free round currently means hand-commenting the guards out of `mercurius.yaml` and restoring them afterward, which is exactly the friction the flag removes.

Open design questions:

- Whether a fresh-reader round consumes the normal finding budget.
- Whether it is operator-triggered only, or suggested near convergence as a late-arc sanity pass.

## Related

- `docs/current/` (configuration, architecture) — the shipped `settled_decisions` guards that the fresh-reader and completeness rounds both lean on.
- `settled-decisions-followups.md` — the deferred richer-entry shape for those guards.
- `web-monitor-and-trajectory.md` — trajectory visualization and reading convergence across rounds.
