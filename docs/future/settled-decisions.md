# Settled decisions and live review context

> Status: implemented (2026-06-01). The behavior described here now ships and is
> documented in `docs/current/` (see the user guide, the new agent guide,
> configuration, MCP tools, and architecture docs). This document persists as the
> record of design intent, not as pending work. Only the items in the Deferred
> section below remain future: the fresh-reader round, the richer
> `{decided, reason}` entry shape, the first-class amend tool, and
> panel/polish/follow-up.

## The friction that started this

The signal came from the field. A planning agent drove a single spec and work
order through roughly fourteen rounds, and reported back that the reviewer kept
re-raising decisions that had already been settled — out-of-scope items
especially — even after explicit "do not flag X" guards had been written into
`review_context`. The only reliable cure was to close the session and open a new
one, which felt like rebooting a machine that had gotten stuck.

The reported mental model was that the reviewer's framing goes *sticky* within a
session: that accumulated context outweighs the guard, and the reviewer slowly
stops listening. That model is wrong, and the way it is wrong is the whole point
of this change.

Rounds are single-shot. The reviewer is a fresh subprocess every round, and
nothing carries between rounds — no prior findings, no decisions log, no triage
history. There is no accumulation, so there is nothing to go stale. Every round,
a brand-new reviewer reads the *same* `review_context` and reaches its own
conclusions cold.

The actual mechanism is duller and more fixable. `review_context` is read from
`mercurius.yaml` exactly once, when the session is opened, and then frozen on the
session for the rest of its life. When the operator hits a recurring re-raise,
adds a guard to the YAML, and runs another round, the reviewer never sees the new
guard — it is still being handed the context captured at session open. The edit
was real; it just never arrived. close-and-reopen "works" only because opening a
session is the one moment the file is read again.

So the reviewer was never stubborn. It was reading yesterday's instructions,
faithfully, every time.

## The deeper problem underneath

Fixing the staleness is easy. But chasing it down exposed something worth fixing
properly: `review_context` has been quietly doing two different jobs, and the two
jobs have opposite shapes.

The first job is **calibration** — a statement of what kind of review this is.
The deployment model, the stakes, the altitude to read at, the
simplicity-versus-defensiveness preference. "Personal tool, single supervised
implementer, pre-1.0, breaking changes fine." This is stable. It is true in round
one and still true in round fourteen, and it primes nothing — it frames the
review without tilting the verdict.

The second job is a **settled-decisions ledger** — a growing list of things the
reviewer should stop raising. "We already decided to defer recall; do not flag
its absence." This is the opposite of stable. It accretes one entry at a time as
the arc progresses, and each entry is, in effect, an instruction to *not see*
something. It is also the part that bloated one project's config past four
hundred lines, and the part that nudges the reviewer away from being a cold fresh
reader and toward being a primed witness who has been told what not to mention.

These two things share a field, share a lifecycle in the operator's head, and
pull in opposite directions. Calibration wants to be set once and left alone.
Guards want to be edited constantly and read fresh. Calibration is neutral; guards
are, by their nature, suppressive. Keeping them in the same free-form blob is what
makes the blob both unwieldy and impossible to reason about — you cannot ask "show
me just the guards" or "run this round as if the guards weren't there," because the
system has no idea which sentences are which.

The fix, then, is two moves: split the jobs apart, and make the file live.

## What changes

### Calibration and guards become separate fields

`review_context` reverts to its first job alone: calibration prose, the stable
framing of what kind of review this is. The guards move out into a new structured
field, `settled_decisions`. After this change, a guard never lives in
`review_context`; calibration never lives in `settled_decisions`. The two can then
be reasoned about, edited, and even toggled independently — which is what makes
everything else here possible.

### `settled_decisions` is a structured, minimal field

```yaml
settled_decisions:
  - id: recall-deferred
    do_not_flag: >
      the absence of a 'recall' concept, or suggestions to add it now
  - id: observability-out-of-scope
    do_not_flag: >
      missing production-grade observability or multi-tenant concerns
```

Each entry carries exactly two things, and the minimalism is deliberate.

`do_not_flag` is the load-bearing text — the actual instruction the reviewer
acts on. It renders into the prompt as its own block, kept distinct from the
calibration block, under a plain heading that tells the reviewer these are
decisions already made and not to be re-raised.

`id` is a handle, and it stays on the operator's side of the fence — it does not
need to reach the reviewer at all. Its job is to let a human find a guard, edit
it, or remove it when a decision stops being settled. It is also the name a guard
takes when it is *born*: the usual way a `settled_decisions` entry comes into
existence is that the reviewer raises something, the operator rejects it as out of
scope in that round's notes, and then — to stop it coming back — promotes that
rejection into a permanent guard. That promotion is a deliberate human act, not
something the system does on its own.

There is, pointedly, no `decided` field and no `reason` field. The temptation to
record *what* was decided and *why* is real, and a richer entry would help the
reviewer distinguish a settled concern from a genuinely-new adjacent one. But the
field's worst failure mode is bloat — guards added speculatively, reasons written
at length, a ledger that grows faster than the artifacts it guards and slows every
cold read without buying anything. The minimal entry is a defense against that.
A guard should cost almost nothing to write, so that the cost of writing a bad one
is also almost nothing to undo.

### The config is read live, every round

`mercurius.yaml` is re-read at the start of every round — `review_context`,
`review_focus`, and `settled_decisions` all refreshed from disk before the prompt
is assembled. The session stops freezing context at open; `open_session` becomes a
thinner thing whose job is to create the container, not to capture a snapshot of
calibration that will drift out of date the moment the operator edits the file.

This is the move that actually fixes the original friction. An edit to a guard
takes effect on the very next round. The close-and-reopen ritual disappears, not
because the reviewer changed, but because the file finally gets read when it
matters.

Reading live also means a round can now fail on a file that has stopped parsing — a
half-finished edit, a transient unreadable moment. Today that class of error only
bites the next `open_session`; once the read moves per round, a broken edit between
rounds fails the next `start_review_round` instead. That is the right place to fail,
and it should fail cleanly: the round does not start, the error names the YAML
problem, and the session is left intact so the operator fixes the file and runs the
round again. A failed re-read is a retryable speed bump, never a session-ending event.

One reporting wrinkle falls out of the same move. The `review_context_present` and
`review_focus_present` flags on `open_session`, `session_status`, and the session
synopsis describe calibration as a session-level property — but once the file is read
per round, "present at open" no longer guarantees "present at round five." They stay,
read as an at-open informational snapshot rather than a session-wide guarantee, and
the docs say so.

### Editing the file is the amend

A natural next ask — and one the field feedback raised explicitly — is a
first-class way to *amend* the settled context mid-arc: some tool call that
revises a decision without restarting anything. With live re-read in place, that
tool is unnecessary, because hand-editing `settled_decisions` between rounds *is*
the amend. Open the YAML, add or remove a guard, run the next round; the change is
live. The file is the interface. Adding a tool on top of it would be ceremony
around something that is already a one-line edit.

### The audit trail survives for free

Reading the config live means a round's context can now differ from the round
before it — which would normally be a problem for a system that prides itself on
being able to show exactly what each reviewer saw. It is not a problem here,
because every round already snapshots its fully-assembled prompt to `_prompt.md`
before dispatch. "What guards was this specific round run with" is therefore
already recorded, per round, as a side effect of how rounds already work. The
snapshot model was built for exactly this kind of per-round variation, and live
re-read simply starts using a capability that was already there.

One deliberate addition goes a step further: each round also snapshots the raw
`mercurius.yaml` it read, beside the rendered prompt. The prompt snapshot shows the guards as
they rendered; the config snapshot shows them as they were written — so a guard that rendered
to nothing, present in the source but absent from the prompt, stays diagnosable from the round
directory. That input-beside-output pairing is the diagnostic the live-read model wants, and it
sits on top of the for-free prompt snapshot rather than replacing it.

### The bootstrap template stops teaching the conflation

The starter config that `mercurius bootstrap` writes is where the conflation was
learned in the first place. Its `review_context` comment currently instructs the
operator to record "locked decisions" there, and describes the field as the thing
that "suppresses findings that do not materially apply" — which is precisely the
guard job, written into the calibration field's own documentation. An agent
configuring a fresh session does what the template tells it, so the template has
been manufacturing the exact problem this change exists to fix.

The template needs three corrections. The `review_context` comment is scoped back
to calibration alone — posture, stakes, scope, simplicity-versus-defensiveness,
and nothing about locked decisions or suppression. A new, commented
`settled_decisions` block is added that explains the field holds decisions already
made, shows the minimal `{id, do_not_flag}` shape, and warns that guards should be
earned by actual re-litigation rather than added speculatively — the
over-extension failure mode, named where the agent will read it. And a short note
records that the file is re-read every round, so edits between rounds take effect
without reopening the session.

The starter's own example should demonstrate the split rather than embody it.
Today its `review_context` ends with "out of scope: production-grade observability
and multi-tenant concerns" — a guard in calibration's clothing. That clause moves
into `settled_decisions` as a worked example entry, so the template shows an agent
both fields standing in their proper roles instead of one field doing both jobs.

## Scenarios

**The recurring out-of-scope re-raise.** The arc is reviewing a spec for a tool
that has deliberately deferred a "recall" feature. Round three, the reviewer flags
the absence of recall as a gap. The operator rejects it — out of scope, decided —
and, because this is the kind of thing a fresh reviewer will keep noticing, adds
`recall-deferred` to `settled_decisions`. Round four reads the updated file, sees
the guard, and says nothing about recall. No session restart, no four-round game
of whack-a-mole.

**Amending a decision that stops holding.** Late in the arc, the operator changes
their mind: recall is back on the table after all. They delete the
`recall-deferred` entry from the YAML. The next round reads the file without it,
and the reviewer is free to raise recall again — now correctly, because it is no
longer settled. The guard was always just a line in a file; un-deciding is as cheap
as deciding was.

## Migration

Existing configs that have accreted guards into the `review_context` prose need a
one-time cleanup: the guards move into `settled_decisions`, and `review_context`
reverts to calibration only. The current example config already shows the
conflation in miniature — its `review_context` ends with "out of scope:
production-grade observability and multi-tenant concerns," which is a guard
wearing calibration's clothes. After migration, that clause becomes an
`observability-out-of-scope` entry in `settled_decisions`, and the calibration
prose is left to describe only posture and stakes.

This is a manual, mechanical pass per project. There is no automatic migration;
the configs are short, the judgment about which sentences are guards is a human
one, and the cleanup is a good occasion to drop guards that were added
speculatively and never earned their place.

## Deferred (and why)

**The fresh-reader round.** Once calibration and guards are separate fields, a
"fresh-reader round" becomes almost free to build: a `start_review_round`
parameter that suppresses the `settled_decisions` block for that round alone,
keeping calibration, to test whether the artifacts hold up on their own merits
rather than under the accumulated permission slips. This is a genuinely useful
verification move and it is exactly what the separation makes cheap — but it is
deferred. Guards earn their keep on real arcs, so this is an occasional check, not
part of the core loop, and close-and-reopen already approximates it today (it
drops nothing from the file, but it does give a reviewer that never saw the arc's
triage history). It belongs in a later iteration, kept in `docs/future/` until
then.

**A richer entry shape.** `decided` and `reason` fields were considered and
rejected, for the bloat reasons above. If real use shows the minimal entry causes
the reviewer to over-suppress — going blind to adjacent concerns it should still
raise — that is the signal to revisit. Until that shows up in practice, minimal
holds.

**A first-class amend tool.** Unnecessary given live re-read, as described above.

**Panel reviewers, polish mode, follow-up-on-one-finding.** All out of scope here.
Panel mode already has its own note in `docs/future/`; polish mode and a per-finding
follow-up channel are named here only as out-of-scope ideas, not specced further.
Folding any of them into this change would muddy a focused one.

## Triage discipline

Three adjustments to how mercurius shapes the one-finding-at-a-time walk. All of
them live in the per-round triage strings mercurius returns
(`oneFindingTriageGuidance` and the parallel `collectedRoundNextAction`), and none
are enforceable — mercurius cannot see the conversation, so this is prompting,
paired with a companion warning in the planning-pipeline guidance.

**Add a gate before acting.** A recurring failure across planning agents is
skipping the discussion turn: the agent forms a confident prediction of what the
operator will decide, treats present-and-wait as overhead, and applies a fix — or
batches several — before the operator has weighed in. Confidence is not consent,
and the prediction is sometimes wrong; the discussion turn is where the operator's
judgment surfaces and where a framing occasionally gets sharpened past what either
side had named. The current guidance already asks the agent to discuss and to stop
and wait, so the problem is not volume — it is placement. The stop sits after
"implement the fix once you and the user are aligned," so an over-confident agent
self-certifies alignment, implements, and honors the stop only afterward. The fix
is a gate ahead of the action: present the finding and its proposed resolution,
then stop and wait for the operator's actual response, and implement only once
they have responded.

**Keep the gate that prevents advancing.** The new decision gate does not replace
the existing one. After a finding is handled, the agent still stops before
advancing to the next finding, recording notes, or calling another mercurius tool,
until the operator responds. One finding per turn, both coming and going: the agent
neither acts before a decision nor moves on after one without an explicit response.
This is what preserves a fresh turn and tool-call budget for each finding.

**Compress the explanation harder.** Each finding and its proposed solution should
be reduced to the clearest, plainest, fewest-words version the agent can produce —
hedges stripped, jargon removed, the reviewer's prose distilled to its essence so
the operator can make a fast yes/no call. The current guidance gestures at this
("clearly and simply, using few words"), but in practice operators still end up
asking for more compression than the agent volunteers. The guidance should bias
hard toward radical plain-language compression as the default, not a fallback, so
the operator does not have to ask for it every round.

The decision-gate discipline is also the one that cannot be carried by prompting
alone, since mercurius cannot enforce a turn it cannot see. So it should also be
written as a standing warning in the planning-pipeline guidance (a grimoire note),
where it rides in the agent's base instructions rather than only in the per-round
triage block. The prompting changes are in scope for this work order; the grimoire
note is a small companion edit.

## A portable agent guide for outside users

Mercurius is starting to be used by people outside this practice, and they do not
have our grimoire. The disciplines that make the loop work well for us — the triage
cadence, the consent gate, radical compression, reading convergence by trajectory
rather than count, the calibration-versus-guards model — live, in their most
articulated form, in grimoire notes those users will never see. The repo's existing
`docs/current/user-guide.md` carries a good deal of it already, but it is written
for the human operator ("ask the design agent to..."), not as something the operator
can hand directly to the agent as operating instructions.

The grimoire's `agent-roles` note tangles two layers. The first is how to drive a
mercurius review well: the one-finding cadence, the two gates, confidence-is-not-
consent, compression to the plainest version, and reading convergence by the
trajectory of finding severity rather than by count. That layer is fully portable —
every mercurius operator needs it. The second layer is how this practice's
four-phase pipeline routes work between a design agent, a planning agent, and an
implementation agent, with its spec-and-work-order convention and its grimoire
promotion model. That layer is ours, and outside users should not inherit it.

The work order ships a new `docs/current/agent-guide.md`: a short, imperative,
agent-facing distillation of the first layer, with the second layer deliberately
filed off. It is the document an outside operator feeds their agent so the agent
drives the loop the way ours do. It carries the triage cadence and both gates, the
consent discipline, the compression default, the dual stop-signal model — take
`ready_to_build` when the reviewer gives it, never chase zero findings, and when
the verdict does not come cleanly stay present and judge whether what remains is at
the noise floor (reading the trajectory of finding severity) — and the
settled-decisions model (calibration versus guards, guards earned by re-litigation
rather than added pre-emptively). It also folds in two disciplines the field
surfaced: a self-audit pass over the artifacts before treating them as done — the
reviewer compares them against code, not against their own earlier sections, so
cross-section drift slips past it — and the test for productive-versus-noise
re-litigation, since a reviewer re-raising a settled point sometimes sharpens the
framing and sometimes is just noise. It does not mention the pipeline, the agent
roles, the spec-and-work-order split, or the grimoire. `mercurius bootstrap` keeps emitting
only `mercurius.yaml`, but its success message points the operator at this guide, so
a newcomer without our grimoire still finds the path to using mercurius well.

This is a `docs/current/` deliverable, so it is written during the docs
re-synthesis that lands when this work order's behavior ships — it documents the new
`settled_decisions` field and the revised triage discipline, so it cannot precede
them. The same docs pass also corrects the existing `user-guide.md`, which today
carries the same two defects this work fixes elsewhere: it tells operators to put
"locked decisions" in `review_context` and calls that field the thing that
"suppresses findings" (the conflation), and its walk-the-findings section places the
stop after the fix rather than before the decision (the gate-placement bug). Agent
guide, user-guide correction, and the bootstrap output pointer are one coherent docs
slice of the work order.

## Related work folded into the same work order

This spec is the design-altitude piece, but the field feedback that prompted it
also surfaced a tail of flat defects with no design content — they need doing, not
deciding. They belong in the same work order as this change so the planning phase
picks them up together, grounded fresh against the code:

- The `decisions` array passed to `record_round_notes` is dropped on the first
  call a majority of the time, and only persists on a second identical call.
  Decisions should persist reliably on the first call. This one is reproduce-first,
  not a known fix: the broker records a populated `decisions` array correctly when it
  arrives, so the drop lives at the MCP input-schema or client boundary rather than in
  the broker. The work order should reproduce it and capture the actual first-call
  payload before committing to a fix, and carry the possibility that constraining the
  input schema does not, on its own, cure it.
- The `decision ref is unknown` error names neither the offending ref's context
  nor the valid refs for that round. It should list the valid refs so the operator
  is not left grepping the round log for the id format.
- The disposition values (`fixed` / `rejected` / `deferred`) are pinned only in Go
  code, exposed in no schema, and discoverable only by reading source. Disposition is
  a field of the human decision recorded through `record_round_notes`, not part of the
  reviewer output schema — so it should become a documented enum on that tool's input
  schema and in the current docs, where a client sees the valid values directly. (This
  may also cure the dropped-decisions bug, if the cause turns out to be a client
  malforming the first call against an unconstrained schema.)

Separately, and as part of the same arc of work, the grimoire's
`software/mercurius/mercurius-vision` note has been reconciled to the single-shot
reality this spec builds on. It previously described decisions feeding forward
into the next round's prompt and a convergence signal mercurius emits — both
removed when the model was simplified to single-shot rounds, and both now
corrected in the note.
