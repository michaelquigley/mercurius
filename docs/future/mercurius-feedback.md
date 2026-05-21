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
