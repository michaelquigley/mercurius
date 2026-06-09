# Fidelity Gate

The standard round asks whether the artifacts are ready to build. Every deferred mode in `review-round-modes.md` asks a variant of that question of the *artifacts* — the current text, an earlier version, the declared scope, the artifacts with guards dropped. A fidelity gate asks the one question none of them can, because it lives on the other side of the handoff: did the build honor the plan? It compares the implementation against the work order that specified it, and surfaces where the code drifted from the design — or where the design was silent and the implementer had to invent.

Nothing here is implemented. This is a deferred mode and a deferred extension of what mercurius reviews; the standard round on design artifacts is the only thing that exists today.

## Why it is a different question

The existing modes are parameterized on a comparison-target axis that never leaves the artifacts: current text, prior snapshot, declared scope, real code surface. The fidelity gate pushes that axis one step past the loop itself — it compares against an artifact produced *after* review concludes: the implementation. That makes it the first mode whose reference frame is the realized code rather than the design.

It completes a progression of consistency checks the pipeline already runs at widening scope:

- The **planning agent's pre-handoff self-audit** checks consistency across sections of one artifact version — space, within a document.
- A **diff / drift round** checks consistency across versions of the artifacts over time — the design loop's own history.
- A **fidelity gate** checks consistency between the plan and its realization in code — the build.

Each widens the frame the previous one could not see past. The self-audit cannot catch a late edit that contradicts an early decision; the diff round catches that but cannot see the code; the fidelity gate sees whether any of it survived contact with implementation.

## The gap it closes

Mercurius emits `ready_to_build`. That verdict is a *prediction* — that an implementer can build from these artifacts without inventing architecture or risking a materially different result. The broker never learns whether the prediction held. The handoff is a one-way cliff: the work order goes over the wall, implementation happens elsewhere, and no signal flows back. So the reviewer cannot calibrate its own bar. If `ready_to_build` artifacts routinely produce a dozen implementation escalations, the readiness threshold is miscalibrated and nothing in the system says so.

The class of miss this targets is the one that survives the entire pipeline — design, planning, review, `ready_to_build` — and is discovered only mid-implementation, after the planning cost has been paid. A motivating real case: a work order specified attaching to a dial tunnel once at startup but did not, in its first form, say *why* the attach had to be decoupled from the per-connection dial. The rationale was load-bearing — re-coupling it would reintroduce a latency regression and a teardown bug — but it lived in the planner's head, not the artifact. It was written down only because the human pushed on it during triage. An implementer who never asked would have "simplified" it straight back into the bug, faithfully implementing a plan that quietly omitted its own constraint. Nothing in the artifact was *wrong*; the standard reviewer found nothing; the gap was in what the plan left tacit.

## A gate, not a post-mortem

The instinct is to call this a retrospective — review the implementation against the plan once the work is done, and learn for next time. That framing loses most of the value. A post-mortem is run after the outcome is fixed; its only product is notes. A fidelity gate runs while the outcome can still change: *does this diff realize the plan, and if it drifted, fix it before it merges.* The per-implementation protection is the main event. The cross-project learning — were the escalations forced or avoidable — is a byproduct harvested from the gate's records, not its reason for existing.

This also fixes the timing. The design-build pipeline deletes the spec and work order once implementation is realized; their value migrates into `docs/current/` and the code. A literal post-mortem would run after its own evidence had been removed. The gate must fire *before* that teardown, while spec, work order, and the diff all still coexist — as the closing step of implementation, the last check before the intent documents are retired. That it has to run while the plan is still alive to check against is itself the tell that it is a gate and not an autopsy.

## The anchor

The comparison target is the work order, with the spec as the rail behind it. These are two different fidelity questions:

- **Plan-fidelity** — diff against the work order. "Did we build it the way we planned?" Concrete and checkable: the work order names files, symbols, and slicing decisions, and the diff either honored them or did not.
- **Intent-fidelity** — diff against the spec. "Did we build the right thing?" The check that catches a faithful implementation of a work order that itself drifted from the vision.

The work order is the precise instrument; the spec is the sanity rail. A gate anchored only on the spec ratifies whatever the work order became; one anchored only on the work order can bless a plan that already left the vision behind. Run both, lead with the work order.

## Forced divergence versus avoidable gap

The signal the gate exists to produce is a judgment call, and getting the distinction right is the whole design. "The implementer diverged from the plan" splits into two opposite things:

- **Forced divergence** — the plan was reasonably silent, or reality moved out from under the read (an SDK behaved differently than the artifacts assumed). The divergence is a *good* escalation; the right response is often to amend the plan, not the code.
- **Avoidable gap** — the planner held the context and did not write it down (the decoupling rationale above). This is the planning defect the gate is hunting.

Only the second is a failure of the artifacts. Distinguishing them is not automatable — which is correct, because human-in-the-loop triage is exactly mercurius's shape. But it means this is not free data: it is another triage surface, one finding at a time, not a number on a dashboard. The danger is building it as a metric engine that counts divergences without judging them, which would measure churn and call it quality.

## The calibration loop

Once the gate exists, each run produces, per finding, a disposition that also answers a question the broker has never been able to ask: *was `ready_to_build` right?* A pattern of avoidable gaps surviving into implementation is direct evidence that the readiness bar — or the `review_focus` calibration, or the planning discipline — needs to move. This is the feedback the one-way cliff currently swallows.

The aggregate version of this — *planners systematically under-specify this category of thing, across projects* — is a larger commitment and should be deferred. It needs a corpus, and mercurius deliberately does not retain one: session state is not persisted as a queryable history, by design. Cross-project pattern-mining wants a persistence layer the broker has chosen not to have. Build the per-project gate first; reach for aggregation only once the records exist and the want is felt in practice. (The trajectory work in `web-monitor-and-trajectory.md` is the nearest existing surface for reading signal off accumulated round artifacts, and is the natural place that aggregation would eventually attach.)

## Expected shape

The gate runs on the same harness as every other mode — a fresh ephemeral reviewer, structured output, a round log, per-round snapshots — and differs only in what it compares against and what the reviewer is told to look for.

- A round mode in which the reviewed artifacts are the work order (and spec) plus the **implementation diff** — a git range, a branch, or a supplied patch — rather than the design artifacts alone.
- The reviewer's posture is fidelity, not taste: *does this diff realize the work order; where it departs, is the departure a forced divergence the plan should absorb, or an infidelity the code should correct?* It is explicitly not a general code review — bugs and style are `/code-review`'s job; this round judges plan-versus-realization only, and scoping it tightly is what keeps it from sprawling into one.
- Output reuses the current schema. Findings are infidelities and divergences rather than design defects; `disposition` (`fixed` / `deferred` / `rejected`) carries the same weight, with the forced-versus-avoidable judgment recorded in the note.
- The round log records the round as a fidelity round and identifies the diff baseline, the same way a diff round identifies its snapshot.
- It runs as the closing gate of implementation, before the `docs/future/` artifacts are retired — the last point at which the plan still exists to check against.

## Sharpenings

- **Surface for deliberation, not auto-correction.** A divergence is a flag to discuss, exactly as in a diff round. Sometimes the plan was wrong and the code is right; the gate's job is to make the gap legible, not to regress the implementation toward a stale plan.
- **Reading the code pulls the reviewer past the line it normally precedes.** The completeness round already names this tension for its code-surface anchor. The fidelity gate lives entirely on that side of the line — it cannot do its job without reading implementation — which is precisely why it is a distinct, late mode rather than a flavor of the standard pre-build review.
- **The diff is the artifact; resist reviewing the whole repo.** Anchoring on the change set keeps the round bounded and keeps the reviewer answering "did this work realize this plan" rather than auditing the codebase at large.
- **A clean fidelity round is the real `ready_to_build` confirmation.** The standard verdict predicts buildability; a fidelity round with no avoidable gaps is the first evidence the prediction held. That is the signal worth reading across an arc, not the raw count of divergences.

## Open design questions

- Where the diff comes from: a git range the operator supplies, the implementation agent's branch, or a patch handed to `start_review_round` as an artifact. Reading a live repo is more faithful but couples the broker to a working tree it otherwise never touches.
- Trigger: close-of-implementation only, or also available mid-implementation as the work lands in slices? Early implementation is legitimately partial, the same way early design rounds are — "did this realize the plan" is a meaningful question only once a coherent slice exists.
- Whether a fidelity round consumes the normal finding budget, and whether it should iterate to convergence (plan and code reconciled) the way the design loop converges artifacts, rather than running once.
- How hard to draw the boundary against `/code-review`. The value is concentrated in plan-fidelity; the moment the round starts flagging bugs the plan never spoke to, it has become a code reviewer wearing mercurius's clothes.
- Whether this is a mode of mercurius or a sibling tool. The round-mode family already treats new comparison-targets as in-scope for the broker, which argues for a mode. The one thing that stretches that precedent is that this mode reads code produced after the loop — the same stretch the completeness round's code-surface anchor flagged. If reading implementation ever pulls enough new machinery behind it, that is the signal to split it out.

## Related

- `review-round-modes.md` — the round-mode family this is a member of; the diff/drift and completeness modes are its nearest siblings, and the self-audit/diff-round progression continues here.
- `settled-decisions-followups.md` — the guards a fidelity round would inherit to suppress intentional divergences the operator already accepted.
- `web-monitor-and-trajectory.md` — where reading the calibration signal across many gated arcs would eventually attach.
- `docs/current/` (architecture, reviewer-output) — the harness and schema a fidelity round reuses unchanged.
