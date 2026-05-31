# Work order: settled decisions and live review context

> Status: future. Nothing here is implemented yet. This is the implementation-shaped
> translation of [`settled-decisions.md`](./settled-decisions.md) — the design spec —
> grounded against the code as it stands. The spec is the record of intent; this work
> order is the plan an implementation agent executes. Where the two disagree, the spec
> governs intent and this document governs implementation; flag any genuine conflict
> rather than silently choosing.

## What this covers

The spec bundles four loosely-coupled changes that arrived from one feedback round.
This work order keeps them in one document but slices them into four independently
buildable, independently verifiable units:

- **Slice A** — split calibration from guards (`settled_decisions`), re-read the
  config live every round instead of freezing it at session open, and snapshot each
  round's raw config for diagnostics. The split and live re-read are the real change;
  the rest (presence-flag handling, the per-round config snapshot) hangs off it.
- **Slice B** — three flat defects: the unhelpful `decision ref is unknown` error, the
  undocumented disposition enum, and the dropped-decisions bug (reproduce-first).
- **Slice C** — three triage-discipline edits to the per-round guidance strings.
- **Slice D** — docs re-synthesis, the bootstrap template, the bootstrap success-message
  pointer, the new portable `agent-guide.md`, and one companion edit in the grimoire.

Build order: **A**, then **B** and **C** (independent of A and of each other), then **D**
last — D documents the behavior A/B/C ship and must not precede them. Within B, **B2 and
B3 are coupled and ordered**: B3 must first reproduce and capture the bug under the current
inferred schema, *then* B2's explicit schema lands as the candidate fix (it is both the enum
mechanism and the leading fix), *then* B3 verifies. Applying B2 before capturing the
reproduction would change the very surface that causes the bug and destroy the evidence.

---

## Grounding: the code as it stands

So the implementer and the reviewer share the same map. Every claim below is anchored to
current code.

**Calibration is read once at open and frozen.** The MCP layer re-reads `mercurius.yaml`
on each `open_session` via `ConfigCalibrationProvider` (`internal/mcpserver/mcpServer.go:256`),
passes `review_context` / `review_focus` into `OpenSessionRequest`, and the broker stores
them on the session (`internal/broker/broker.go:154`). Every round copies them off the
session into the round job (`internal/broker/broker.go:226`) and into `prompt.Build`
(`internal/broker/broker.go:273`). An edit to the YAML mid-session never reaches a round —
this is exactly the staleness the spec diagnoses. The plumbing to re-read from disk already
exists; it is just wired to open, not to round-start.

**The conflation is baked into three places.** The round prompt tells the reviewer to
"Suppress concerns that do not realistically apply under the stated deployment constraints,
implementation model, **locked decisions**, or out-of-scope boundaries"
(`internal/prompt/prompt.go:42`). The bootstrap template instructs the operator to record
"locked decisions" in `review_context` and calls that field the thing that "suppresses
findings" (`internal/bootstrap/mercurius.yaml:20-21`, body clause at `:25`). The user guide
repeats it (`docs/current/user-guide.md:34`).

**The audit trail already captures per-round context.** Each round writes its fully-assembled
prompt to `_prompt.md` before dispatch (`internal/broker/broker.go:280`). Per-round context
variation is therefore already recorded; live re-read needs no new audit machinery.

**`decision ref is unknown` drops the valid refs on the floor.** The error carries only the
offending ref (`internal/broker/broker.go:493`); the valid set is right there in `round.refs`
(`internal/broker/broker.go:71`, populated by `refsFromOutput` at `:921`).

**Disposition is pinned only in Go.** `validDisposition` hardcodes `fixed`/`rejected`/`deferred`
(`internal/broker/broker.go:917`). The `record_round_notes` input schema is inferred by
reflection from `RecordRoundNotesInput` (`internal/mcpserver/mcpServer.go:126`); the
`Disposition` field carries no enum. The go-sdk's `jsonschema` struct tag only sets a
field *description*, not an enum (`google/jsonschema-go/jsonschema/infer.go:330-338`), so the
struct-tag path alone cannot constrain the value. The SDK does honor an explicitly-provided
`Tool.InputSchema` (`*jsonschema.Schema`) instead of inferring (`go-sdk/mcp/server.go:242-261`,
`:289-294`), and it validates incoming arguments against the resolved schema — so an enum set
there is enforced at the SDK boundary before the broker ever sees the call.

**The triage gate sits after the action, not before it.** `oneFindingTriageGuidance`
(`internal/mcpserver/mcpServer.go:642`) says "implement the fix in the artifacts once you and
the user are aligned. Then stop and wait." The stop is downstream of the implement; an
over-confident agent self-certifies alignment and acts before the human weighs in. The
parallel `collectedRoundNextAction` (`:649`) has the same ordering.

**Config field-name conversion.** `dd` auto-converts CamelCase to snake_case
(`df/dd/df.go:127` `toSnakeCase`). Verified: `ID` -> `id`, `DoNotFlag` -> `do_not_flag`,
`SettledDecisions` -> `settled_decisions`. The new fields need **no** `dd` struct tags.

**No in-repo config to migrate.** The only tracked `mercurius.yaml` is the bootstrap template
(`internal/bootstrap/mercurius.yaml`); there is no committed root config. The repo's `.mercurius/`
is session logs, not config. External user configs migrate manually per the spec's Migration
section — out of scope for code.

---

## Slice A — calibration / guards split + live per-round re-read

### A1. New config field

`internal/config/config.go`:
- Add `SettledDecisions []SettledDecision` to `Config` (`:26`).
- New type: `type SettledDecision struct { ID string; DoNotFlag string }`. No `dd` tags
  (verified above). `dd.MergeYAMLFile` populates it automatically.
- `Validate()` (`:83`): do **not** hard-fail on empty or duplicate entries. An entry whose
  `do_not_flag` is empty after trim is ignored at prompt assembly (A4), not a load error;
  `id` is operator-side and never reaches the reviewer, so duplicate ids are not validated.
  Rationale: the spec's minimalism principle ("a guard should cost almost nothing to write")
  plus the live-re-read failure model (A3) — a too-strict validator would fail rounds on
  trivial guard typos. Decided (review round 1, c-002): tolerate; the detectability concern an
  empty guard would otherwise hide is handled by the A6 per-round config snapshot, not by
  validation.
- `checkRenamedFields` (`:116`) needs no entry — `settled_decisions` is new, not renamed.

### A2. Carry guards through the per-round path

The read moves from open to round-start, kept in the MCP layer so the broker stays
config-agnostic (it takes injected calibration, never reads YAML itself).

`internal/mcpserver/mcpServer.go`:
- Extend `CalibrationProvider` (`:206`) to also return settled decisions, and the raw config
  bytes for the per-round snapshot (A6):
  `func(ctx) (reviewContext, reviewFocus string, settled []broker.SettledDecision, raw []byte, err error)`.
- `ConfigCalibrationProvider` (`:256`) maps `config.SettledDecision` -> `broker.SettledDecision`
  and returns the raw bytes it read (A6).
- **Call the provider in `start_review_round`** (`:313`), not just in `open_session`, and pass
  the fresh values into `StartRoundRequest`. This is the move that makes edits live.
- `open_session` (`:279`) keeps calling the provider, but only to populate the at-open
  presence booleans on the response (A5). It no longer freezes calibration for rounds.

`internal/broker/types.go`:
- New `broker.SettledDecision struct { ID, DoNotFlag string }` (mirrors the `Artifact` pattern
  that already spans broker/prompt/reviewer).
- `StartRoundRequest` (`:49`): add `ReviewContext string`, `ReviewFocus string`,
  `SettledDecisions []SettledDecision`.
- `OpenSessionRequest` (`:32`): keep `ReviewContext`/`ReviewFocus` (now used only to compute
  at-open presence); no settled-decisions needed at open.

`internal/broker/broker.go`:
- `session` struct (`:48`): replace the frozen `reviewContext`/`reviewFocus` **text** fields
  with at-open presence booleans (`reviewContextPresentAtOpen`, `reviewFocusPresentAtOpen`).
  The session no longer holds calibration text.
- `roundJob` struct (`:92`): add `settledDecisions []SettledDecision` (it already carries
  `reviewContext`/`reviewFocus`).
- `StartReviewRound` (`:177`): build the round job from `req.ReviewContext` /
  `req.ReviewFocus` / `req.SettledDecisions` instead of from the session (`:226-227`).
- `executeRoundJob` (`:262`): pass `SettledDecisions` into `prompt.Build` (`:273`).

### A3. Live-read failure handling

Re-reading per round means a round can now fail on a mid-edit syntax error or a transient
unreadable file. `open_session` already maps a provider error to a clean `user_error`
("reread mercurius.yaml", `mcpServer.go:283`). `start_review_round` must do the same: if the
provider errors, return `user_error` whose message/details name the YAML problem, do **not**
start the round, and leave the session active so the operator fixes the file and retries. A
failed re-read is a retryable speed bump, never a session-ending event (spec, "The config is
read live, every round"). The provider error happens in the MCP handler *before*
`b.StartReviewRound`, so no broker state is touched on this path — keep it that way.

### A4. Render the settled-decisions block; de-conflate calibration

`internal/prompt/prompt.go`:
- `Request` (`:22`): add `SettledDecisions []SettledDecision` (+ a `prompt.SettledDecision`
  type, mirroring `prompt.Artifact`).
- **Reword the calibration weighting sentence** (`:42`): drop "locked decisions" so the
  calibration block stays neutral framing — "...under the stated deployment constraints,
  implementation model, or out-of-scope boundaries." Suppression-by-prior-decision is now the
  settled-decisions block's job, not calibration's.
- **Add a distinct block** after the `## Review context` block (and its weighting sentence),
  before `## What to flag`. Render only when at least one entry has a non-empty `do_not_flag`.
  Plain heading the reviewer can't miss, e.g. `## Settled decisions (do not re-raise)`, with a
  lead-in stating these are decisions already made and out of scope — do not raise them as
  concerns, questions, or advisory notes, and do not suggest revisiting them — followed by one
  bullet per `do_not_flag`. `id` is **not** rendered (operator-side handle only). Entries with
  empty `do_not_flag` are skipped.
- Keep the block as its own field/section so the deferred "fresh-reader round" (suppress
  guards for one round) stays a cheap one-flag addition later — do not entangle it back into
  the calibration string.

### A5. Presence flags become an at-open snapshot

With calibration read per round, `review_context_present` / `review_focus_present` are no
longer stable session properties. Keep them as an at-open informational snapshot (the value
when the session opened), and say so in the docs (Slice D). Touch points:
`OpenSessionResponse` (`broker/types.go:38`), `SessionStatusResponse` (`:150`), the synopsis
(`synopsisEntryFromSession`, `broker.go:603`), and their MCP outputs. Do **not** add a
settled-decisions presence flag — keep the surface minimal.

### A6. Snapshot the raw config per round

Each round snapshots the raw `mercurius.yaml` it ran with into the round directory as
`_config.yaml` (`_`-prefixed, like `_prompt.md` and the other broker meta files). This
completes the per-round input/output diagnostic pair: `_config.yaml` is what was fed in,
`_prompt.md` is what it rendered to. A guard that rendered empty — present as an entry in
`_config.yaml` but absent from the `_prompt.md` settled-decisions block — is therefore
diagnosable after the fact from the round directory. This is the chosen response to the
detectability concern behind c-002 (review round 1): we do **not** validate `do_not_flag`
(tolerate-and-ignore stands, per A1), we make a silently-dropped guard visible in the audit
trail instead.

Read once, parse and snapshot from the same buffer, so `_config.yaml` is exact by construction.
Not because a mid-read rewrite is likely — one driver, who is also the one editing the file —
but because a single buffer removes the two-reads ambiguity (best-effort adjacent read vs exact
input) for free:
- Add a single-read loader (`config.LoadWithRaw` / `LoadBytes`): read the config file once into a
  buffer, parse calibration and run `checkRenamedFields` from that same buffer, and return both
  the parsed config and the exact bytes. The extended `CalibrationProvider` (A2) uses it and
  returns those bytes; nothing re-reads the file. (This also retires `config.Load`'s current
  double read of the file.)
- `StartRoundRequest` (`types.go:49`): add `RawConfig []byte`; the `start_review_round` handler
  passes the provider's bytes in.
- `roundJob` (`broker.go:92`): add `rawConfig []byte`.
- `executeRoundJob` (`broker.go:262`): write `rawConfig` to `_config.yaml` in the round dir,
  next to where it already writes `_prompt.md` (`:280`).

Caveat: the raw config can carry `reviewer.binary_path` / `extra_args` / `model`. `.mercurius/`
is gitignored and local, so the exposure is low; name it in the docs (Slice D) so it is not a
surprise.

Reconciliation with the spec: this extends the spec's "audit trail survives for free" framing.
The prompt snapshot remains the for-free part — the spec's claim that per-round guards are
recorded stays true — and the raw-config snapshot is a deliberate diagnostic addition on top
of it, decided in review round 1. The spec's audit-trail paragraph is updated to acknowledge it.

### A — done criteria

- A guard added to `mercurius.yaml` appears in the **next** round's `_prompt.md` with no
  session reopen; an edit to `review_context` likewise takes effect next round.
- The rendered calibration block no longer instructs suppression-by-locked-decision; that
  instruction appears only in the settled-decisions block, and only when guards are present.
- A round started against a broken/unreadable YAML returns `user_error` naming the problem,
  the session stays active, and a retry after fixing the file succeeds.
- Presence flags reflect the at-open snapshot and are documented as such.
- Each round's `_config.yaml` holds the exact bytes of the config that produced that round's
  prompt; a guard present in `_config.yaml` but absent from `_prompt.md` is diagnosable as a
  rendered-empty guard (the c-002 detectability response).

### A — tests

- `config_test`: parse `settled_decisions` (snake_case keys via `dd`), empty-`do_not_flag`
  entry tolerated, duplicate `id` tolerated.
- `prompt_test`: block present when guards present, absent when empty/none; `id` never
  rendered; calibration sentence reworded (no "locked decisions").
- `broker_test`: round job reads calibration from `StartRoundRequest`, not the session;
  presence booleans set at open; `_config.yaml` written per round, byte-identical to the
  config the round was started with.
- `mcpserver_test`: provider returns guards; `start_review_round` re-reads live (a changed
  provider value reaches the round); provider-error path returns `user_error` without starting
  a round.

---

## Slice B — three flat defects

### B1. `decision ref is unknown` lists the valid refs

`internal/broker/broker.go:493`: add the round's valid refs to the error details, e.g.
`details{"ref": decision.Ref, "valid_refs": sortedRefs(round.refs)}` (new helper returning the
sorted keys of `round.refs`). Keep the message stable; the details carry the fix so the
operator stops grepping the round log. **Done:** error surfaces the valid refs.
**Test:** `broker_test` asserts `valid_refs` is present and sorted on an unknown-ref decision.

### B2. Disposition enum on the `record_round_notes` input schema

Provide an explicit `Tool.InputSchema` (`*jsonschema.Schema`) for `record_round_notes`
(`mcpServer.go:355`) instead of relying on struct inference, with the full input described —
`session_id`, `round_number`, `commentary`, and a `decisions` array whose items declare `ref`
(string), `disposition` (string with `Enum: ["fixed","rejected","deferred"]`), and `note`
(string). Because the SDK validates arguments against the resolved schema
(`server.go:242-261`), an invalid disposition is now rejected at the boundary; keep
`broker.validDisposition` (`:917`) as a defense-in-depth backstop. Also name the three values
in the `disposition` field description (the struct-tag path supports description) so a client
reading the schema sees them in prose too. **Done:** the registered tool advertises the enum;
an invalid disposition is rejected before the broker. **Test:** `mcpserver_test` asserts the
registered `record_round_notes` input schema carries the disposition enum and the fully-typed
`decisions` array.

### B3. Dropped-decisions — reproduce first

The broker records a populated `decisions` array correctly when it arrives (`RecordRoundNotes`,
`broker.go:466`), so the drop lives at the MCP input-schema / client boundary, not in code we
can see. Sequence, in order:

1. **Reproduce and capture the actual first-call payload** the client sends **under the
   current inferred schema** — before B2 changes that schema (test harness or recorded
   transcript). Do not skip to a fix.
2. **Confirm the root cause** against that payload. The leading hypothesis is that the
   reflection-inferred `decisions` array is under-specified, so a client omits/malforms it on
   the first call and gets it right on the retry.
3. **Apply the B2 explicit schema as the candidate fix** (it fully types the `decisions`
   array) and re-run the reproduction.
4. If the reproduction is gone, done. **If it is not** — i.e. the cause is elsewhere (e.g.
   go-sdk behavior) — **escalate with the captured payload; do not invent a workaround.** The
   spec is explicit that constraining the schema may not, on its own, cure it.

**Done:** a documented reproduction exists; decisions persist on the first call in it; or, if
unfixed by the schema, an escalation with evidence rather than a silent patch.
**Test:** a regression test that drives the first call the way the reproduction does and
asserts decisions persist.

---

## Slice C — triage-discipline strings

`internal/mcpserver/mcpServer.go`, two functions, mirrored edits:

- `oneFindingTriageGuidance` (`:642`) and `collectedRoundNextAction` (`:649`):
  1. **Gate before the action.** Present the finding and its proposed resolution, then **stop
     and wait for the operator's actual response, and implement only after they respond** —
     move the stop ahead of the implement (today it sits after).
  2. **Keep the advance gate.** After a finding is handled, still stop before advancing to the
     next finding, recording notes, or calling another tool, until the operator responds. One
     finding per turn, both coming and going.
  3. **Compress harder by default.** Reduce each finding and its proposed solution to the
     plainest, fewest-words version — hedges stripped, jargon removed — as the default, not a
     fallback the operator has to ask for.
- `noFindingsTriageGuidance` (`:645`): no findings to gate; leave unless a wording tweak is
  warranted for consistency.

**Done:** both strings place the decision gate before the fix and name radical plain-language
compression as the default. **Tests:** update the existing substring assertions
(`mcpserver_test.go:329-330` assert "explain the finding and its proposed solution clearly and
simply, using few words" and "implement the fix") to match the reordered, compression-biased
wording.

> Not enforceable by code — mercurius can't see the conversation. The standing-warning half of
> this discipline rides in the grimoire planning-pipeline guidance (Slice D5).

---

## Slice D — docs re-synthesis, template, guide, grimoire note

Lands **after** A/B/C, since it documents shipped behavior.

### D1. Bootstrap template — `internal/bootstrap/mercurius.yaml`

- Scope the `review_context` comment (`:18-21`) to calibration alone — posture, stakes, scope,
  simplicity-vs-defensiveness — and remove "locked decisions" and the "suppresses findings"
  framing.
- Move the `out of scope: production-grade observability and multi-tenant concerns` clause
  (`:25`) out of the `review_context` body and into a new commented `settled_decisions` example
  as a worked entry, so the template demonstrates the split instead of embodying the
  conflation.
- Add the commented `settled_decisions` block: it holds decisions already made; minimal
  `{id, do_not_flag}` shape; guards earned by actual re-litigation, not added speculatively
  (name the over-extension failure mode where the agent will read it); and a note that the file
  is re-read every round, so edits between rounds take effect without reopening the session.
- `bootstrap_test.go`: update expected content.

### D2. Bootstrap success message — `cmd/mercurius/bootstrap.go:28`

After "wrote '%s'", add a pointer to `docs/current/agent-guide.md` for how to drive a review
well, so a newcomer without the grimoire finds the path. Update the bootstrap command test.

### D3. `docs/current/` updates

- `configuration.md`: add `settled_decisions` to Optional Fields + the full example; reword the
  `review_context` description (`:42`) to calibration-only; update the "edit the YAML before
  opening a session" note (`:48`) to reflect live per-round re-read (edits take effect next
  round, no reopen).
- `user-guide.md`: fix the conflation at `:34` (calibration-only; introduce `settled_decisions`
  as the home for decided-and-out-of-scope items); split the `out of scope` clause out of the
  example config (`:19-25`); fix the gate placement at `:103` (stop before the fix); reflect
  live re-read where it says calibration is edited before a session (`:32`).
- `mcp-tools.md`: note the disposition enum is now schema-visible on `record_round_notes`
  (`:105`); clarify `review_context_present` / `review_focus_present` as an at-open snapshot
  (`:42`, `:121`); note `start_review_round` re-reads calibration live.
- `reviewer-output.md`: no disposition-enum change needed (disposition isn't reviewer output);
  confirm the advisory-disposition note (`:75`) still reads correctly.
- `docs/current/README.md` (docs index): add `agent-guide.md`.

### D4. New `docs/current/agent-guide.md` — portable, grimoire-free

Short, imperative, agent-facing. The document an outside operator hands their agent so it
drives the loop the way ours do. Include: the one-finding triage cadence; both gates
(before-action and before-advance) and confidence-is-not-consent; radical compression as
default; the dual stop-signal model (take `ready_to_build` when given; never chase zero
findings; when the verdict doesn't come cleanly, stay present and judge whether what remains
sits at the noise floor, reading the trajectory of finding severity); the settled-decisions
model (calibration vs guards; guards earned by re-litigation, not pre-emptive); a self-audit
pass over the artifacts before treating them done (the reviewer compares against code, not
against the artifact's own earlier sections, so cross-section drift slips past it); and the
productive-vs-noise re-litigation test. **Do not** mention the four-phase pipeline, the agent
roles, the spec/work-order split, or the grimoire.

### D5. Grimoire companion note — OUTSIDE this repo

In the grimoire's `practice/creative/agent-roles`, add the decision-gate standing warning to
the planning-agent guidance — confidence is not consent; gate before action — so it rides in
the agent's base instructions, not only in the per-round triage block (the discipline code
can't enforce). This is a grimoire edit, not a repo file. (The `mercurius-vision`
single-shot reconciliation the spec mentions was already done — past tense in the spec — so it
is not pending work here.)

### D6. Reconcile the future docs after the behavior lands

Repo rule 3: once the current docs above capture the shipped behavior, re-synthesize the
implemented future material so `docs/future/` no longer describes as 'future' what now exists.
The implemented design from `settled-decisions.md` and this work order folds into `docs/current/`;
what stays live in `docs/future/` is only the still-deferred set — the fresh-reader round, the
richer `{decided, reason}` entry shape, the first-class amend tool, and panel/polish/follow-up
(the spec's Deferred section). The spec and work order persist as the record of intent (per the
design-build pipeline), but marked implemented, not pending.

### D — done criteria

`go build`, `go vet`, `go test ./...` green. No doc or template still tells operators to put
locked decisions in `review_context` or places the stop after the fix. `agent-guide.md` exists
and contains no grimoire/pipeline/role references. The template demonstrates the
calibration/guards split. The bootstrap success message points at the guide. The grimoire
planning-agent guidance carries the confidence-is-not-consent gate-before-action warning
(D5) — the one deliverable no in-repo check (build/vet/test/grep) covers. And `docs/future/` no
longer describes the shipped `settled_decisions` behavior as future — only the spec's Deferred
items remain live there (D6).

---

## Cross-cutting

**Dependency order.** A first. B and C are independent of A and of each other. D last
(documents A/B/C). Within B: reproduce-and-capture (B3 step 1, under the current inferred
schema) **before** B2's explicit schema — applying B2 first destroys the reproduction
evidence — then B2, then B3's rerun and regression test. B1 is independent. D5 is cross-repo.

**Migration.** Code-side, only the bootstrap template (D1). No committed root config; the
`.mercurius/` directory is session logs and is untouched. External user-config migration is
manual and out of scope for code (spec, Migration).

**Out of scope** (mirrors the spec's deferrals — do not build): the fresh-reader round (a
`start_review_round` flag suppressing the guards block for one round — A4 keeps it cheap to add
later, but it is not built here); the richer `{decided, reason}` entry shape; a first-class
amend tool (live re-read makes hand-editing the file the amend); panel reviewers, polish mode,
and per-finding follow-up.

**Whole-arc verification.** Beyond unit tests: bootstrap a temp config, open a session, run a
round against the `dummy` reviewer, add a `settled_decisions` guard, run a second round, and
confirm the new guard appears in round two's `_prompt.md` with no reopen. Then introduce a YAML
syntax error and confirm `start_review_round` fails cleanly with the session still active.

## Self-audit note

This work order was drafted against the code at the current HEAD and re-read end-to-end for
cross-section consistency before handoff. The spec was patched in the same session to correct
the disposition "output schema" framing, reframe the dropped-decisions defect as
reproduce-first, and name the live-re-read failure mode and the presence-flag ripple — so spec
and work order agree on those four points rather than drifting.

It was then revised across two mercurius review rounds. Round 1 corrected the B2/B3 ordering
(reproduce before the schema change), and resolved the empty-guard detectability concern by
adding the A6 per-round config snapshot rather than validating guards. Round 2 made the A6
snapshot exact by construction (single read, one buffer), folded the settled O-1/O-2 calls into
the body and removed the open-questions section, and added D6 for post-ship future-doc
reconciliation.
