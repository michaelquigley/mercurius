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

`open_session` accepts an optional `review_focus` parameter, symmetric to the existing `review_context` override. When present, the value replaces the config's `review_focus` for that session's rounds. The session-status response includes `review_focus_source` (`"config"` | `"session"`) and `review_focus_present` for parity with the existing `review_context_source` and `review_context_present` fields.

`start_review_round` does not accept a per-round override (deferred).

### Advisory refs in `record_round_notes`

`record_round_notes` currently returns `unknown_ref` for any `decisions[].ref` that isn't a concern or question id. Relax this: advisory note ids (typically `a1`, `a2`, ...) are valid refs and accept the same disposition vocabulary.

Advisory dispositions feed into the decisions log and into the prior-decisions block of future review prompts, the same way blocking-finding dispositions do. The reviewer prompt's existing instruction ("treat accepted, rejected, and deferred decisions as adjudicated session context, do not re-raise") generalizes to advisories without changes other than mention of the new `fixed` disposition introduced in the next section.

### `fixed` disposition

The disposition enum gains a fourth value: `fixed`. Vocabulary becomes:

- `accepted`: agreed; not yet addressed in artifacts.
- `fixed`: agreed and addressed in artifacts.
- `rejected`: disagreed.
- `deferred`: agreed but not addressing in this session.

`accepted` is retained for the case where the team agrees but the artifact change has not happened yet (or won't happen as part of this session). `fixed` is the most common disposition in normal use and should be preferred when an artifact edit was made.

The reviewer prompt's prior-decisions instruction generalizes to all four dispositions: "Treat accepted, fixed, rejected, and deferred decisions as adjudicated session context. Do not re-raise these items unless the artifacts now make the prior decision concretely broken or there is a genuinely new angle."

### `next_finding` by severity

`collect_round.triage.next_finding` currently defaults to the first concern by id, then the first question if no concerns are present. Change to: highest-severity concern first (blocker > major > minor), ties broken by id order; questions follow concerns. `next_finding` remains advisory; the design agent can pick another ref.

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

Coexist. There is a real distinction between "we agree but haven't acted" and "we agreed and acted." Both occur in practice, especially when a finding is accepted in principle but the corresponding artifact change is scheduled for later.

**Q: Should `record_round_notes` enforce that advisory dispositions only use a subset of the vocabulary?**

No. The four dispositions all generalize to advisory items. Reviewer-prompt treatment is identical.

**Q: Should `mercurius preview` be exposed as an MCP tool too?**

No, not in v1. The user iterating on a prompt is a human running a CLI; the design agent doesn't need to call preview during a session. If a use case emerges later, the MCP wrapper is a thin addition.

**Q: Should the reviewer prompt's prior-decisions instruction differentiate the four dispositions?**

No. Treating all four equivalently for re-raise purposes is simpler and consistent. Once a finding has been adjudicated under any disposition, the reviewer leaves it alone unless the artifacts have moved.

## Open questions

None blocking implementation.
