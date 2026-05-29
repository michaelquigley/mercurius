# Mercurius and planning-pipeline feedback

Notes from driving a single multi-artifact planning arc end to end:
spec + work order for a new "practices" concept in flo, taken from
blank page to `ready_to_build`. Roughly fourteen review rounds across
four sessions, mixing Codex as the reviewer and one human operator as
the decision-maker. This is field feedback, not a postmortem write-up.

## What worked well

- **Asymptotic-convergence framing.** Naming the goal as "findings
  shrink in scope" rather than "findings go to zero" kept the loop
  from churning. The signal for completion was clean: when round 1 of
  a fresh session returned `ready_to_build` after the work order had
  been through more than ten prior rounds, the stop was obvious.
- **Background-round + re-engage-on-completion.** Right fit for
  multi-minute reviewer calls. No polling needed; the operator just
  pinged "round landed."
- **Settled context as a re-litigation guard.** The "do not flag the
  absence of X" pattern in `review_context` is the load-bearing
  device. Without it, a deferred concept ("recall") and a removed
  CLI surface (`--practice` flag) came back four times each across
  the arc. Once the guards were comprehensive, both stopped resurfacing.
- **Caveman-mode finding walkthroughs.** Compressing each finding to
  its essence let the operator make fast yes/no decisions. Without
  that compression, walkthroughs would have been hedge-bloated and
  slow, and a fourteen-round arc would have been intolerable.

## Pain points worth fixing

- **Reviewer re-raises settled decisions even with explicit guards.**
  The most reliable workaround was close-session + open-session,
  which forces `mercurius.yaml` to be re-read. The reviewer's
  framing appears to get sticky within a session — accumulated context
  outweighs the explicit guard. A first-class "refresh settled context
  mid-arc" affordance, or an architecture without session-level
  caching, would eliminate the workaround.
- **`decision ref is unknown` error doesn't say what refs are valid.**
  Each round required grepping the round file for the actual id format
  (sometimes `C1`, sometimes a long slug-style id). The error should
  list valid refs alongside the rejection.
- **`decisions` array silently dropped on first
  `record_round_notes` call**, requiring a second identical call.
  Happened roughly 60-70% of the time across the arc. No visible
  cause; the workaround (call again with the identical payload)
  felt like beating a slot machine. Worth a look at the parameter
  parsing.
- **Disposition strings undocumented.** `"fixed" / "rejected" /
  "deferred"`, not `"fix" / "reject" / "defer"`. Discovered by
  reading source. The JSON schema should pin the enum.
- **No follow-up channel within a round.** For findings where the
  operator wanted to interrogate the reviewer — "are you sure about
  C2? did you consider X?" — the only path was record-and-close
  the round, then open a new one with the same artifacts. That is
  an expensive round-trip when the answer might be thirty seconds
  of back-and-forth. A narrow "respond to one finding" tool would
  cover the case without compromising round atomicity.
- **`review_context` grew to dominate `review_focus`.** Across the
  arc it became a settled-decisions ledger longer than the rest of
  the YAML combined. The format wasn't designed for that — it
  worked, but a separate top-level `settled_decisions` field with
  structured entries (decision, ref, reason, "do not flag" guards)
  would scale better than appending paragraphs to free-form prose.
- **`session_status` is underused.** Useful for finding the right
  `round_number` when recording notes, but never surfaced as a
  natural step in any workflow. Could be promoted by including
  the round number directly in `start_review_round`'s response so
  the recorder doesn't have to look it up.
- **Settled-context over-extension is its own failure mode.** The
  counterpart to "settled context as a re-litigation guard": guards
  added speculatively, before any real re-litigation has occurred,
  bloat the YAML and slow reviewer cold-reads without buying
  anything. In one arc the initial `mercurius.yaml` carried ~11
  settled-decision blocks inherited from a prior session and ran
  ~400 lines; the operator asked for simplification, and trimming
  to ~50 lines made round 1 of the new arc faster to read and
  faster to dispatch. Guards should be earned by actual reviewer
  re-litigation, not pre-emptive. Comprehensive at project-posture
  altitude; minimal at PR-review altitude.

## Planning-pipeline integration

These notes are about how mercurius fits into the broader planning-agent
workflow described in the grimoire, not about mercurius proper.

- **Spec/work-order altitude split is genuinely productive.** Vision-
  altitude spec stays readable; implementation-altitude work order
  stays grounded. When the planning agent drifted — re-explaining the
  model inside the work order, or writing algorithm steps inside the
  spec — the reviewer flagged it. The two artifacts serve different
  readers and don't bleed into each other when the altitude rule is
  enforced.
- **Grounding fidelity is the highest-value reviewer output.** Across
  the arc, multiple rounds caught cases where the planning agent
  named a function that didn't exist, got a signature wrong, or
  referenced a file that had been moved. Forcing the planning agent
  to read code rather than write to a mental model is worth more
  than any single architectural finding the reviewer surfaced.
- **Iteration shape is front-loaded.** Early rounds carried
  architectural concerns requiring real thinking. Late rounds carried
  narrow detail polish. Two different cognitive modes, same tool.
  A lighter-weight "polish mode" for late-arc rounds would burn fewer
  tokens per round without losing signal.
- **Single-reviewer-per-round limits angle diversity.** A reviewer
  focused on operator-experience would catch different things than
  one focused on code grounding. The architecture supports it
  (`reviewer:` is one config block) but the round shape does not —
  parallel rounds with different reviewer configs, and triage that
  merges findings across reviewers, would extend the loop without
  restructuring it.
- **No structural support for replanning.** If the operator comes back
  next session wanting to change a settled decision, the workflow is
  hand-edit the work order, hand-edit `mercurius.yaml`, run a round.
  That works, but there is no first-class "amend the settled context"
  action. Today the ledger is informal markdown inside the YAML.
- **Convergence stop signal is qualitative.** `ready_to_build` with
  zero concerns is unambiguous, but "two advisories and one concern
  that's a true edge case" is judgment. A built-in heuristic — "if
  findings have not changed character across two rounds, declare
  convergence" — would help the planning agent recognize when
  further rounds are extracting diminishing returns.
- **Close-and-reopen approximates the fresh-reader round.** The
  meta-observation at the bottom of this doc proposes an
  empty-`review_context` round to test artifact durability. That
  mode isn't implementable today, but close-current-session +
  open-new-session is a partial approximation: `review_context`
  still loads, but the reviewer doesn't carry round-by-round triage
  history. Used after a consistency-fix pass at the end of one arc;
  surfaced three precision advisories the original session hadn't
  named, validating that the artifacts hold up under fresh-reader
  scrutiny. Planning agents should know this pattern is available
  today without waiting for mercurius to grow a dedicated mode.
- **Intra-artifact consistency drift is the planning agent's job,
  not the reviewer's.** Across a multi-round arc the spec and work
  order accumulated stale framing in earlier sections — a round-1
  conclusion paragraph that under-stated scope after rounds 4-7
  pulled in more work; a thread that was explicitly reframed in a
  later round but never updated where it lived; a critical-files
  table that drifted into direct contradiction with later sections.
  Mercurius reviewers compared artifacts against code, not against
  later sections of the same artifact, and none of these surfaced
  via mercurius review (not even in the fresh-eyes session). They
  emerged only when the planning agent did a self-directed
  end-to-end re-read prompted by the operator. Worth adding to
  planning-pipeline guidance: a self-audit pass over both artifacts
  between the final mercurius round and the implementation handoff.
  Look for stale framing in early sections, under-stated scope
  statements, "may need to" hedges that have since been resolved,
  and tables that haven't kept pace with body edits.
- **Confidence is not consent.** The most reliable planning-agent
  failure mode in one arc was the agent's own tendency to skip the
  present-and-wait step once the operator had shown a clear pattern
  of agreement. The cognitive shape: form a high-confidence
  prediction about what the user will say; the discussion turn
  looks like overhead; batch advisories or apply edits before
  asking. Twice in one session the operator pulled the agent back.
  The discussion turn is where the user's judgment surfaces, where
  framings get sharpened, and where predictions are sometimes wrong.
  Even a tiny extra turn is cheaper than an unconsented edit. The
  pattern is durable enough across planning agents that it's worth
  flagging as a standing warning in the planning-pipeline guidance.
- **Re-litigation isn't always noise — sometimes it surfaces
  refinement.** Nuance to "reviewer re-raises settled decisions":
  when the reviewer re-raises a decision, the right response isn't
  always "yes, settled, here's why." Sometimes walking the operator
  through the re-raise surfaces a sharper framing than the original
  decision had. In one arc, a round-3 re-raise of a regression test
  dropped in round 2 produced a third framing (assertion of a
  permanent contract, not regression guard against a deleted path)
  that was better than either earlier round's resolution. Test for
  productive vs. noise re-litigation: does walking the operator
  through it surface something neither side had named? If yes,
  productive. If the operator just confirms the prior decision
  verbatim, it's noise — and the guard pattern applies.

## One meta-observation worth its own line

The largest unmoderated risk in this loop is **the reviewer becoming
an LLM playing a part rather than a fresh reader.** When the planning
agent adds "do not flag X" guards to the settled context, the reviewer
mostly obeys — but there is no way to know whether it is *understanding*
the context or merely suppressing flags it would otherwise have raised.
A periodic "fresh-reader round" — same artifacts, but with an empty
`review_context` for that round alone — would test whether the spec
and work order hold up on their own merits, not just under the
accumulated permission slips. That single addition is the one I would
most want to see in the planning-pipeline guidance.
