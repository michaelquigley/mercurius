# Review Loop Ergonomics (work order)

## Reference

Spec: `docs/future/review-loop-ergonomics-spec.md`.

## Milestones

### M1: Per-session `review_focus` override

**Files**

- `internal/mcpserver/mcpServer.go` — `open_session` MCP tool gains an optional `review_focus` parameter. Pass through to broker.
- `internal/broker/broker.go` — `OpenSession` request struct grows a `ReviewFocus` field. When non-empty, the session stores it and uses it in prompt assembly; otherwise the config's `review_focus` is used. Session state tracks `review_focus_source` and `review_focus_present` parallel to the existing `review_context_source` and `review_context_present`.
- `internal/broker/types.go` — types extended.
- `internal/broker/broker_test.go` — coverage for config-only, session-override, and source-reporting cases.
- `internal/mcpserver/mcpServer_test.go` — `open_session` covers the new parameter.
- `docs/current/mcp-tools.md` — `open_session` request shape gains `review_focus`; response shape gains `review_focus_source` and `review_focus_present`.

**Done when**

- `open_session.review_focus` is accepted and used in subsequent rounds.
- Session status reports `review_focus_source` and `review_focus_present` symmetrically to review_context.
- Tests cover the override path and the config-only path.

### M2: Advisory refs in `record_round_notes`

**Files**

- `internal/broker/broker.go` — relax the ref-validation logic in `RecordRoundNotes` to accept advisory ids in addition to concern and question ids. Advisory dispositions are recorded the same way and feed into the decisions log.
- `internal/broker/broker_test.go` — coverage for advisory ref acceptance, advisory disposition rendering in `decisions.md`, and advisory disposition presence in the prior_decisions block of the next round's prompt.
- `internal/roundlog/roundLog.go` — if the round log notes section serializes decisions, ensure advisory refs render correctly.
- `docs/current/mcp-tools.md` — `record_round_notes` documentation updates: `decisions[].ref` accepts concern, question, or advisory ids.
- `docs/current/reviewer-output.md` — note that advisory notes also flow into decisions when dispositioned.
- `docs/current/user-guide.md` — "Record Notes and Decisions" section example or note that advisories can also be dispositioned.

**Done when**

- `record_round_notes` accepts advisory refs without `unknown_ref` errors.
- Advisory dispositions appear in `decisions.md` and in the prior_decisions block of the next round's prompt.
- Documentation reflects that advisories are valid decision refs.

### M3: `fixed` disposition

**Files**

- `internal/schema/reviewOutput.go` — disposition enum gains `fixed`.
- `internal/schema/reviewOutputSchema.json` — enum updated.
- `internal/schema/reviewOutput_test.go` — coverage for the four-element enum.
- `internal/broker/broker.go` — disposition validation accepts `fixed`. The `invalid_decision` error message lists all four.
- `internal/broker/broker_test.go` — coverage for `fixed` acceptance and rejection of arbitrary other strings.
- `internal/prompt/prompt.go` — prior-decisions instruction prose updates to: "Treat accepted, fixed, rejected, and deferred decisions as adjudicated session context. Do not re-raise these items unless the artifacts now make the prior decision concretely broken or there is a genuinely new angle."
- `internal/prompt/prompt_test.go` — assertion updates.
- `docs/current/user-guide.md` — disposition vocabulary documentation: add `fixed`. Update the "Record Notes and Decisions" example to use `fixed` where appropriate.
- `docs/current/mcp-tools.md` — `record_round_notes` documentation: add `fixed` to the disposition list.
- `docs/current/operations.md` — `invalid_decision` error description mentions all four dispositions.

**Done when**

- `fixed` is a valid disposition and accepted by `record_round_notes`.
- Reviewer prompt explains that all four dispositions should not be re-raised unless artifacts moved.
- Documentation reflects the four-disposition vocabulary.

### M4: `next_finding` by severity

**Files**

- `internal/broker/broker.go` — `collect_round`'s `triage.next_finding` selection sorts by severity rank (blocker > major > minor) then id order. Questions follow concerns and remain id-ordered.
- `internal/broker/broker_test.go` — coverage with mixed-severity rounds, all-blocker rounds, all-major rounds, concerns + questions mixed.

**Done when**

- `next_finding` defaults to the highest-severity concern, ties broken by id order.
- Questions are selected only when no concerns remain.
- Tests cover the severity sort across the typical configurations.

### M5: `mercurius preview` command

**Files**

- `cmd/mercurius/main.go` — new `preview` subcommand. Reads config, applies CLI overrides, reads each artifact's bytes, computes SHA-256, calls `prompt.Build()` with empty `PriorDecisions`, prints the result. Helper logic may live in `main.go` directly or in a small new package, at the implementer's discretion.
- `cmd/mercurius/main_test.go` — coverage for argument parsing, override application, and output equivalence to `Build()`.

CLI shape:

```
mercurius preview --config <path> \
  [--review-context "..."] \
  [--review-focus "..."] \
  --artifact name=path \
  [--artifact name=path ...] \
  [--max-findings N] \
  [--output <file>]
```

Notes:

- `--config` defaults to `./mercurius.yaml`, consistent with other subcommands.
- `--artifact` may be repeated; each takes the form `name=path` where `name` is the artifact name (subject to the same validation as `open_session`, including the leading-underscore rule from the prompt-philosophy bundle) and `path` is the absolute or working-directory-relative artifact path.
- `--max-findings` overrides the config value for the preview only; defaults to the config's `max_findings`.
- Output is the assembled prompt verbatim. No session is created, no `.mercurius/` writes happen.
- Output goes to stdout by default; `--output <file>` writes to the path.

**Done when**

- `mercurius preview` produces an assembled prompt byte-equal to what `Build()` produces for the same inputs.
- No `.mercurius/` directory state is created.
- Tests verify CLI parsing, override behavior, and output equivalence to `Build()`.

### M6: Docs reframe and cold-start practice note

**Files**

- `docs/current/user-guide.md` — "Decide Whether to Continue" section gains:
  - The `ready_to_build` semantics paragraph from the spec.
  - The cold-start vs continued sessions paragraph from the spec.
- `docs/current/mcp-tools.md` — `close_session` verdict description gains the `ready_to_build` semantics clarification.

**Done when**

- The user guide names the readiness asymptote explicitly.
- The user guide names the cold-start vs continued practice pattern.
- `close_session` documentation matches the user guide's framing.

### M7: Future-doc cleanup

**Files**

- `docs/future/review-loop-ergonomics-spec.md`, `docs/future/review-loop-ergonomics-work-order.md` — once M1-M6 are merged and current docs reflect the implementation, delete these or move any residual content into appropriate current docs.
- `docs/future/mercurius-feedback.md` — keep as a historical artifact.
- `docs/future/calibration-ideas.md` — review and remove any items now superseded; specifically the proposals related to `prompt_overrides` decomposition (already obsolete from prompt-philosophy) and to disposition vocabulary.

**Done when**

- `docs/future/` contains only items that remain genuinely deferred (notably `web-monitor-and-trajectory.md`, `panel-mode-and-diff-rounds.md`, and any remaining stretch entries in `calibration-ideas.md`).

## Test plan

All existing tests pass with updated fixtures. New assertions:

- `open_session` with `review_focus` parameter routes the value into prompt assembly.
- Session status reports `review_focus_source` and `review_focus_present` correctly.
- `record_round_notes` accepts advisory ids as decision refs.
- Advisory dispositions appear in `decisions.md` and the next round's prior-decisions block.
- `fixed` is a valid disposition; reviewer prompt mentions all four in the re-raise suppression instruction.
- `next_finding` returns the highest-severity concern first.
- `mercurius preview` produces a prompt byte-equal to what `Build()` produces for the same inputs.
- `mercurius preview` does not create any `.mercurius/` directory state.

## Sequence

M1 → M2 → M3 → M4 are roughly orthogonal; sequential matters mostly for clean commit history. M5 (preview) follows because its preview output should reflect any prompt changes from M3 (the four-disposition prior-decisions text). M6 (docs) follows the code. M7 is housekeeping after M1-M6 land.

## Out of scope

- Per-round prompt overrides on `start_review_round` (separate later bundle).
- Web monitor / trajectory analytics (see `docs/future/web-monitor-and-trajectory.md`).
- Stretch items: prompt-effect tracking, multi-variant fix proposals.
- `posture` field as structured config.
- An MCP-tool surface for `preview` (CLI only in v1).
