# Review Loop Ergonomics (spec)

## Context

Several small items from `mercurius-feedback.md` remain after the prompt-philosophy and schema-simplification bundles landed. Each is a real friction point observed in actual review sessions, but each is small enough that shipping in isolation is overhead. This bundle groups them.

The grouping is "review-loop ergonomics" — small protocol and tooling changes that sharpen the iteration loop a design agent and a human run through mercurius. None changes the broker's core mechanics; together they reduce ambient friction.

## Goals

- Lift the asymmetries that bit during the prompt-philosophy review session (advisory dispositions, "fixed" vocabulary).
- Make session-scoped prompt experimentation easy without committing variants to mercurius.yaml.
- Default the obvious thing for `next_finding` (highest severity first).
- Give prompt iteration a sighted entry point (preview before round-1).
- Name the readiness asymptote in `close_session` semantics so users don't chase a verdict that isn't coming for the wrong reasons.
- Capture cold-start vs continued sessions as recommended practice.

## Non-goals

- Per-round (not just per-session) prompt overrides — separate scope.
- Web monitor and trajectory analytics — separate scope; see `web-monitor-and-trajectory.md`.
- Stretch items #10 (prompt-effect tracking) and #11 (multi-variant fixes) from the feedback doc.
- `posture` field as structured config. The `review_context` prose continues to do this work in flexible form.

## Design

### Per-session `review_focus` override

`open_session` accepts an optional `review_focus` parameter, symmetric to the existing `review_context` override. The override is applied only when non-empty after trimming whitespace; empty or whitespace-only values are treated as absent and the config's `review_focus` is used. (To clear a configured focus for a single session, edit the config — there is no "clear via override" path. This matches existing `review_context` semantics.) The session-status response includes `review_focus_source` (`"config"` | `"session"` | `"none"`) and `review_focus_present` for parity with the existing `review_context_source` and `review_context_present` fields. `"none"` indicates that neither the config nor the session has provided a `review_focus`. The same trim-and-non-empty rule applies to the `mercurius preview --review-focus` CLI flag.

`start_review_round` does not accept a per-round override (deferred).

### Advisory refs in `record_round_notes`

`record_round_notes` currently returns `unknown_ref` for any `decisions[].ref` that isn't a concern or question id. Relax this: advisory note ids (typically `a1`, `a2`, ...) are valid refs and accept the same disposition vocabulary.

Advisory dispositions feed into the decisions log and into the prior-decisions block of future review prompts, the same way blocking-finding dispositions do. The reviewer prompt's existing instruction (currently "treat accepted, rejected, and deferred decisions as adjudicated session context, do not re-raise") generalizes to advisories with the new vocabulary introduced in the next section ("treat fixed, rejected, and deferred...").

For convergence accounting to remain correct, the broker must distinguish blocking refs (concerns, questions) from advisory refs reliably. The id string is convention, not contract — the reviewer output schema does not enforce id prefixes, and id-name parsing would silently misclassify any ref the reviewer chose to name unconventionally. The broker records each ref's *kind* at the moment a round result is processed, sourced from the array the ref appeared in (`concerns`, `questions`, or `advisory_notes`). Convergence accounting consults the recorded kind, not the ref string. The reviewer is autonomous, model-versioned, and replaceable; naming conventions cannot be relied on as contract.

For the array-sourced kind to be unambiguous, ref ids must be globally unique across the three arrays — no id may appear twice in the same array, and no id may appear in more than one of `concerns`, `questions`, and `advisory_notes`. Reviewer output is rejected with `schema_violation` if any id appears more than once anywhere in those three arrays — alongside the existing readiness-consistency check. This rejects reviewer drift loudly rather than silently absorbing it into a wrong kind.

The reviewer prompt is part of this contract surface. JSON Schema cannot express cross-array uniqueness, so the rule has to be stated to the autonomous reviewer in prose; otherwise the reviewer can emit schema-valid output that fails broker validation at runtime, wasting the round. The prompt's reviewer-output instruction section gains an explicit statement of the rule, and `docs/current/reviewer-output.md` documents it for human readers.

### `fixed` disposition

The disposition vocabulary stays at three elements, with `accepted` replaced by `fixed`:

- `fixed`: agreed and addressed in artifacts.
- `rejected`: disagreed.
- `deferred`: agreed but explicitly not addressing in this session.

The shipped vocabulary today is `accepted` / `rejected` / `deferred`, where `accepted` defaulted to "agreed-and-handled" because it was the only "agreement" disposition. Once `fixed` exists for the agreed-and-acted case, `accepted` has no coherent role in mercurius's session-bounded operation: an unaddressed-but-agreed finding is either being fixed in the same turn (`fixed`), held over for a later session (`deferred`), or being silently left in a state where the reviewer is rightly going to keep flagging the unchanged artifact. Replacing `accepted` with `fixed` removes the ambiguity without losing expressiveness.

The reviewer prompt's prior-decisions instruction becomes: "Treat fixed, rejected, and deferred decisions as adjudicated session context. Do not re-raise these items unless the artifacts now make the prior decision concretely broken or there is a genuinely new angle."

### `next_finding` by severity

`collect_round.triage.next_finding` currently defaults to the first concern by id, then the first question if no concerns are present. Change to: highest-severity concern first (blocker > major > minor), ties broken by lexicographic string order of id; questions follow concerns. `next_finding` remains advisory; the design agent can pick another ref. The `triage.findings` array continues to preserve the reviewer's emitted order — only `next_finding` is sorted by severity. The design agent can re-sort the array itself if needed.

### `mercurius preview` command

A new CLI subcommand:

```
mercurius preview --config <path> \
  [--review-context "..."] \
  [--review-focus "..."] \
  --artifact name=path \
  [--artifact name=path ...] \
  [--max-findings N] \
  [--output <file>]
```

Behavior:

- Reads the config, applies any overrides, reads each artifact's bytes, computes its SHA-256, calls `prompt.Build()` with empty `PriorDecisions`, prints the assembled prompt to stdout (or the file given by `--output`).
- No session is created. No reviewer is dispatched. No round counter is consumed. No `.mercurius/` writes happen.
- Prior decisions are empty (round-1 equivalent). For previewing later rounds, the existing `_prompt.md` log file in the corresponding round's snapshot directory is the canonical artifact to read; `mercurius preview` is for the unsighted round-1 case where there is no prior log to consult.
- The `DecisionsLog` field is set to the same rendered text broker round 1 passes for a session with no rounds. Both broker and preview call a shared pure function for the empty-session form, guaranteeing the preview prompt is byte-equal to round 1's prompt apart from the `SnapshotPath: "(preview)"` sentinel. This factoring is load-bearing: without a single source of truth, the two paths can drift the moment one is edited without the other.

Use case: tune `review_focus` (or other config-shaped content) before paying the cost of a real round.

### Docs reframe: `ready_to_build` semantics

Add to the `close_session` verdict description and to the user guide's "Decide Whether to Continue" section:

> `ready_to_build` does not mean zero findings remain. It means the remaining findings — including any deferred or rejected ones — are below the noise floor for the implementer the artifacts are written for, under the stated `review_context`. The verdict reflects a judgment about implementation readiness, not a guarantee of artifact perfection. Advisory notes in particular do not block readiness.

### Docs: cold-start vs continued sessions

Add a recommended-practice paragraph to the user guide's "Decide Whether to Continue" section:

> Continued sessions and fresh sessions surface different review angles. A continued session has conversational momentum from prior rounds and tends to keep iterating on dimensions it has already explored. A fresh session against the same artifacts sees them cold and can find different surface area. After multiple continued rounds without convergence, opening a fresh session sometimes surfaces findings the continued arc did not reach.

This is documented practice, not a tool feature. No code changes.

## What stays unchanged

- All MCP tool surfaces other than the new `open_session.review_focus` parameter and the relaxed `record_round_notes` ref validation.
- Reviewer interface and reviewer implementations.
- Round log structure, snapshot directory layout, `status.json` shape.
- Schema validation rules other than the disposition enum extension.
- Convergence signal mechanism (still `none | watch | consider_closing`); trajectory analytics are deferred to the web monitor arc.
- The CLI monitor command.
- Round budget mechanics, `max_findings` cap.

## Resolved questions

**Q: Should `fixed` replace `accepted`, or coexist?**

Replace. The four-element vocabulary (`accepted`, `fixed`, `rejected`, `deferred`) was considered first, on the theory that "we agree but haven't acted" and "we agreed and acted" are distinct states. In mercurius's session-bounded operation, the distinction does not survive: the per-finding-per-turn cadence means agreement and action almost always coincide in the same turn, and an "agreed but unaddressed" decision creates a confused state where the artifact still has the issue, the next round's reviewer is justified in re-raising, and the prior-decisions instruction is the only thing suppressing it. The three-element vocabulary (`fixed`, `rejected`, `deferred`) is exclusive on intent: fix it, reject it, or explicitly punt it. Codex's round-4 finding on overlapping `accepted` / `deferred` semantics validated the redundancy by demonstrating it.

**Q: What does the `accepted_decisions` convergence counter count under the new vocabulary?**

It counts `fixed` decisions. The field name `accepted_decisions` is retained for now (pre-1.0; no external consumers; renaming churn outweighs the clarity gain). Documentation updates to say the counter tracks `fixed` decisions specifically. Renaming to `fixed_decisions` (or a neutral name like `addressed_decisions`) is a deferred cleanup for the 1.0 milestone, not blocking this bundle.

**Q: Do advisory decisions count toward the convergence counters?**

No. The convergence counters (`accepted_decisions`, `declined_or_deferred_decisions`) track blocking-finding triage progress. Advisory dispositions are recorded in `decisions.md` and flow into prior-prompts for adjudication carry-forward, but they do not influence the convergence counters. Including advisory dispositions would dilute the counters' "we're making progress on the things that block readiness" semantics — a session could show high accepted-decision counts while blocking findings remain unresolved.

**Q: Should `record_round_notes` enforce that advisory dispositions only use a subset of the vocabulary?**

No. The three dispositions all generalize to advisory items. Reviewer-prompt treatment is identical.

**Q: Should `mercurius preview` be exposed as an MCP tool too?**

No, not in v1. The user iterating on a prompt is a human running a CLI; the design agent doesn't need to call preview during a session. If a use case emerges later, the MCP wrapper is a thin addition.

**Q: Should the reviewer prompt's prior-decisions instruction differentiate the three dispositions?**

No. Treating all three equivalently for re-raise purposes is simpler and consistent. Once a finding has been adjudicated under any disposition, the reviewer leaves it alone unless the artifacts have moved.

## Open questions

None blocking implementation.
