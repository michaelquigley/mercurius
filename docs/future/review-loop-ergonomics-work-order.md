# Review Loop Ergonomics (work order)

## Reference

Spec: `docs/future/review-loop-ergonomics-spec.md`.

## Milestones

### M1: Per-session `review_focus` override

**Files**

- `internal/mcpserver/mcpServer.go` — `open_session` MCP tool gains an optional `review_focus` parameter. Pass through to broker.
- `internal/broker/broker.go` — `OpenSession` request struct grows a `ReviewFocus` field. When non-empty after trimming whitespace, the session stores it and uses it in prompt assembly; otherwise (empty, whitespace-only, or absent) the config's `review_focus` is used. Session state tracks `review_focus_source` and `review_focus_present` parallel to the existing `review_context_source` and `review_context_present`.
- `internal/broker/types.go` — types extended.
- `internal/broker/broker_test.go` — coverage for config-only, session-override, and source-reporting cases.
- `internal/mcpserver/mcpServer_test.go` — `open_session` covers the new parameter; `session_status` covers the new `review_focus_source` and `review_focus_present` fields in the response.
- `docs/current/mcp-tools.md` — `open_session` request shape gains `review_focus`; response shape gains `review_focus_source` and `review_focus_present`. The `session_status` section also documents the new fields, since that is the tool agents read mid-session.

**Done when**

- `open_session.review_focus` is accepted and used in subsequent rounds.
- Session status reports `review_focus_source` and `review_focus_present` symmetrically to review_context.
- Tests cover the override path and the config-only path.

### M2: Advisory refs in `record_round_notes`

**Files**

- `internal/broker/broker.go` — relax the ref-validation logic in `RecordRoundNotes` to accept advisory ids in addition to concern and question ids. Advisory dispositions are recorded the same way and feed into the decisions log and prior-prompts. They do *not* contribute to the convergence counters (`accepted_decisions`, `declined_or_deferred_decisions`), which track blocking-finding triage only.

  When a round result is processed, record each ref's *kind* (concern, question, or advisory) sourced from the array the ref appeared in. Store the kind in the round's ref metadata. `RecordRoundNotes` looks up incoming refs against this metadata and uses the recorded kind for convergence accounting; ref-string parsing is forbidden — the reviewer is autonomous and model-versioned, so naming conventions are not contract.

  Add a reviewer-output validation rule (alongside readiness consistency): reject the round with `schema_violation` if any id appears more than once anywhere in `concerns`, `questions`, and `advisory_notes` — including duplicates within a single array (e.g., two concerns sharing an id) and duplicates across arrays (e.g., `c1` in both `concerns` and `advisory_notes`). Without this rule the kind lookup is ambiguous; with it, reviewer drift fails loudly rather than silently coercing into a wrong kind.
- `internal/broker/broker_test.go` — coverage for advisory ref acceptance, advisory disposition rendering in `decisions.md`, advisory disposition presence in the prior_decisions block of the next round's prompt, confirmation that advisory dispositions do not influence convergence counters, an explicit cross-case where an advisory ref dispositioned as `fixed` leaves both convergence counters unchanged (advisories don't count regardless of disposition value), kind-classification using an advisory id that does not begin with `a` (e.g., `note-1`) — the broker classifies it as advisory because of the array it came from, not the name — and `schema_violation` rejection when reviewer output reuses the same id, both within a single array (e.g., two concerns sharing an id) and across arrays (e.g., `c1` in both `concerns` and `advisory_notes`).
- `internal/roundlog/roundLog.go` — if the round log notes section serializes decisions, ensure advisory refs render correctly.
- `internal/broker/errors.go` — `unknown_ref`'s `next_action` text updated to reflect that advisory ids are now valid decision refs (current text presumes only concerns and questions are valid).
- `internal/prompt/prompt.go` — the reviewer-output instruction section gains an explicit statement of the cross-array uniqueness rule. Suggested wording: "Within a single review output, every id appearing in `concerns`, `questions`, or `advisory_notes` must be unique across all three arrays. Never reuse an id — not within a single array, and not across arrays." This instruction is load-bearing because JSON Schema cannot express cross-array uniqueness; without it, the autonomous reviewer can emit schema-valid output that fails broker validation at runtime.
- `internal/prompt/prompt_test.go` — assertion that the rendered prompt contains the uniqueness instruction (substring check is sufficient).
- `docs/current/mcp-tools.md` — `record_round_notes` documentation updates: `decisions[].ref` accepts concern, question, or advisory ids.
- `docs/current/reviewer-output.md` — note that advisory notes also flow into decisions when dispositioned; document the cross-array id uniqueness rule (every id in `concerns`, `questions`, and `advisory_notes` must be unique across all three arrays; rejection is `schema_violation`).
- `docs/current/user-guide.md` — "Record Notes and Decisions" section example or note that advisories can also be dispositioned.

**Done when**

- `record_round_notes` accepts advisory refs without `unknown_ref` errors.
- Advisory dispositions appear in `decisions.md` and in the prior_decisions block of the next round's prompt.
- Documentation reflects that advisories are valid decision refs.

### M3: `fixed` disposition (replaces `accepted`)

**Files**

- `internal/broker/broker.go` — `validDisposition()` swaps `accepted` for `fixed`; the new vocabulary is `fixed` / `rejected` / `deferred` only. The `invalid_decision` error message lists those three. The convergence-accounting switch in the same file (currently `case "accepted":` increments `accepted_decisions`) becomes `case "fixed":`. The field name `accepted_decisions` is retained for now (renaming is a deferred 1.0 cleanup).
- `internal/broker/broker_test.go` — coverage for `fixed` acceptance; `accepted` is now rejected as invalid (regression-protection for the swap); inclusion of `fixed` decisions in the `accepted_decisions` convergence counter.
- `internal/prompt/prompt.go` — prior-decisions instruction prose updates to: "Treat fixed, rejected, and deferred decisions as adjudicated session context. Do not re-raise these items unless the artifacts now make the prior decision concretely broken or there is a genuinely new angle."
- `internal/prompt/prompt_test.go` — assertion updates.
- `internal/broker/errors.go` — `invalid_decision`'s `message` and `next_action` text lists the three dispositions (`fixed`, `rejected`, `deferred`); any prior reference to `accepted` is removed.
- `docs/current/user-guide.md` — disposition vocabulary documentation reflects the swap: `accepted` is removed, `fixed` is added, the three dispositions are defined as in the spec. Update the "Record Notes and Decisions" example to use `fixed`.
- `docs/current/mcp-tools.md` — `record_round_notes` documentation: disposition list is `fixed` / `rejected` / `deferred`. Note that the `accepted_decisions` convergence counter is named for legacy reasons and counts `fixed` decisions.
- `docs/current/operations.md` — `invalid_decision` error description mentions the three dispositions.

**Done when**

- `fixed` is a valid disposition and accepted by `record_round_notes`. `accepted` is rejected.
- Reviewer prompt explains that the three dispositions should not be re-raised unless artifacts moved.
- Convergence counts `fixed` decisions toward `accepted_decisions`; tests verify.
- Documentation reflects the three-disposition vocabulary; the `accepted_decisions` field name is documented as a retained-for-now legacy and is noted to count `fixed` decisions specifically.

### M4: `next_finding` by severity

**Files**

- `internal/mcpserver/mcpServer.go` — `collect_round`'s `triage.next_finding` selection sorts by severity rank (blocker > major > minor) then lexicographic string order of id. Questions follow concerns and remain id-ordered. The `triage.findings` array preserves reviewer-emitted order; only `next_finding` is severity-sorted. (Triage assembly currently lives in `mcpServer.go`; the broker provides the underlying round result.)
- `internal/mcpserver/mcpServer_test.go` — coverage with mixed-severity rounds, all-blocker rounds, all-major rounds, concerns + questions mixed.

**Done when**

- `next_finding` defaults to the highest-severity concern, ties broken by id order.
- Questions are selected only when no concerns remain.
- Tests cover the severity sort across the typical configurations.

### M5: `mercurius preview` command

**Files**

- `internal/config/config.go` — split `Validate()` into pure field validation (no filesystem mutation) and a new `EnsureLogDestination()` method that performs the existing `ensureLogDestination()` call. `Load()` invokes only `Validate()` and returns side-effect-free; callers that intend to write to `log_destination` invoke `EnsureLogDestination()` separately.
- `internal/config/config_test.go` — coverage for pure-validation behavior (no log-destination creation when only `Load()` is called) and the separate `EnsureLogDestination()` call.
- `cmd/mercurius/main.go` — new `preview` subcommand. Reads config via `config.Load()` only (no `EnsureLogDestination()` call), applies CLI overrides, reads each artifact's bytes, computes SHA-256, calls `prompt.Build()` with empty `PriorDecisions`, prints the result. Helper logic may live in `main.go` directly or in a small new package, at the implementer's discretion. Update the existing MCP server startup path to call `EnsureLogDestination()` explicitly after `Load()` (preserving today's behavior of creating the directory before any session opens). Update the existing `monitor` subcommand to call `Load()` only — no `EnsureLogDestination()` (read-only path; should not create `log_destination`, fixing a latent issue masked by monitor typically running after the broker has already created the directory).
- `cmd/mercurius/main_test.go` — coverage for the preview subcommand (argument parsing, override application, output equivalence to `Build()`, and zero `.mercurius/` state created), plus regression coverage for monitor not creating `log_destination`.
- `internal/broker/broker.go` — factor the empty-session branch of `decisionsLogText()` into an exported pure function (e.g. `EmptySessionDecisionsLogText()`). The existing `(s *session).decisionsLogText()` calls this function for the no-rounds-with-decisions case; preview calls it directly. This is the shared-source-of-truth referenced by the `DecisionsLog` field above.
- `internal/broker/broker_test.go` — coverage that the exported function returns the established empty-session text; coverage that `(s *session).decisionsLogText()` and `EmptySessionDecisionsLogText()` agree for an empty session.
- `docs/current/operations.md` — new "Preview the Round-1 Prompt" section. Covers the CLI shape, repeated `--artifact name=path` syntax, the no-session / no-reviewer / no-`.mercurius/`-writes contract, and a pointer to `snapshots/round-NN/_prompt.md` for previewing later rounds. This is the primary current-docs landing for the feature.
- `docs/current/README.md` — `mercurius preview` added to the "Current Capabilities" list alongside `mercurius monitor`; the "Start Here" entry for `operations.md` updated to mention prompt preview alongside monitoring.
- `docs/current/user-guide.md` — short workflow note that `mercurius preview` is the recommended way to iterate on `review_focus` (or other config-shaped content) before paying the cost of a real round; pointer to operations.md for the full command reference.

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

**Config refactoring (prerequisite to preview's no-write guarantee)**

The current `config.Validate()` includes a filesystem mutation (`ensureLogDestination()` creates `log_destination` if missing). That conflates pure validation with a side effect, which makes any side-effect-free use of `Load()` impossible. Preview is the immediate driver for fixing this; the same latent issue affects `monitor`, which today creates `log_destination` only as a coincidence of running after the MCP server.

Refactor:

- `Validate()` becomes pure: field checks only, no filesystem mutation.
- A new `EnsureLogDestination()` method on `Config` performs the existing `ensureLogDestination()` call.
- `Load()` invokes only `Validate()` and returns a fully-validated config without side effects.
- Callers that need the directory created (the MCP server startup path) invoke `EnsureLogDestination()` explicitly after `Load()`.
- Callers that do not write (preview, monitor) call only `Load()`.

**Preview's `prompt.Request` construction**

The preview command builds a `prompt.Request` with the following fields, deterministically chosen so that "byte-equal to `Build()` output for the same inputs" is well-defined:

- `Artifacts[i].Name`: from the CLI `--artifact name=path`.
- `Artifacts[i].SourcePath`: the artifact path resolved to absolute. Relative paths resolve against the process working directory.
- `Artifacts[i].SnapshotPath`: the literal sentinel string `"(preview)"`. Preview has no snapshot directory; this sentinel makes the absence visible in the prompt without inventing a path. The reviewer doesn't act on snapshot paths, so this is harmless to round behavior and makes the preview status inspectable.
- `Artifacts[i].Hash`: SHA-256 of the artifact's bytes, formatted as the prompt's existing `sha256:<hex>` rendering.
- `Artifacts[i].Content`: the artifact's bytes as read from disk.
- `Artifacts[i].Inline`: false.
- `PriorDecisions`: empty slice.
- `DecisionsLog`: the rendered text of an empty session decisions log, byte-equal to what broker round 1 passes for a session with no rounds. Broker and preview both source this string from a shared exported function (e.g. `broker.EmptySessionDecisionsLogText()` or equivalent factoring), so the empty-session form has a single source of truth and cannot drift between the two call sites. Setting `DecisionsLog: ""` would route through the prompt template's empty-string fallback, producing different output than broker round 1 — that is the bug this contract closes.
- `ReviewContext`: from CLI `--review-context` if non-empty after trimming, otherwise the config's `review_context`.
- `ReviewFocus`: from CLI `--review-focus` if non-empty after trimming, otherwise the config's `review_focus`.
- `MaxFindings`: from CLI `--max-findings` if present, otherwise the config's `max_findings`.

Two preview invocations with the same arguments and the same artifact contents must produce byte-equal output.

Notes:

- `--config` defaults to `./mercurius.yaml`, consistent with other subcommands.
- `--artifact` may be repeated; each takes the form `name=path` where `name` is the artifact name (subject to the same validation as `open_session`, including the leading-underscore rule from the prompt-philosophy bundle) and `path` is the absolute or working-directory-relative artifact path. Parsing splits on the first `=` only (idiomatically via `strings.Cut`), so paths containing `=` are handled correctly.
- `--max-findings` overrides the config value for the preview only; defaults to the config's `max_findings`.
- Output is the assembled prompt verbatim. No session is created, no `.mercurius/` writes happen.
- Output goes to stdout by default; `--output <file>` writes to the path.

**Done when**

- `Validate()` performs no filesystem mutation; pure-validation paths pass through with no `.mercurius/` state created.
- `EnsureLogDestination()` is invoked explicitly from the MCP server startup path before any session is opened.
- The `monitor` subcommand calls `Load()` only and does not invoke `EnsureLogDestination()`.
- `mercurius preview` produces an assembled prompt byte-equal to what `Build()` produces for the same inputs.
- `mercurius preview`'s output for an empty session is byte-equal to broker round 1's prompt for the same artifacts and config, except for `Artifacts[i].SnapshotPath` (preview uses the sentinel `"(preview)"`). A test verifies this directly by constructing an empty session through the broker's prompt-construction path and diffing against preview output.
- No `.mercurius/` directory state is created by `mercurius preview` or `mercurius monitor`.
- Tests verify CLI parsing, override behavior, output equivalence to `Build()`, and the absence of filesystem side effects on the no-write paths.
- `mercurius preview` is documented in `docs/current/`: a new section in `operations.md` covers CLI shape and contract; `README.md` lists it under Current Capabilities; `user-guide.md` includes a short workflow note. M7's future-doc cleanup runs only after these current-docs landings are in place.

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
- Advisory dispositions do not influence convergence counters.
- Ref-kind classification is array-based, not name-based: an advisory id like `note-1` is correctly classified as advisory because it came from `advisory_notes`.
- Reviewer output with duplicate ids is rejected with `schema_violation` — both within a single array (e.g., two concerns sharing an id) and across arrays (e.g., `c1` in both `concerns` and `advisory_notes`).
- Reviewer prompt explicitly instructs the reviewer that ids must be unique across `concerns`, `questions`, and `advisory_notes`; the rendered prompt is asserted to contain the instruction.
- An advisory ref dispositioned as `fixed` leaves both convergence counters unchanged (cross-case test: advisories never count regardless of disposition value).
- `fixed` is a valid disposition; `accepted` is rejected; reviewer prompt mentions the three dispositions in the re-raise suppression instruction.
- `next_finding` returns the highest-severity concern first.
- `mercurius preview` produces a prompt byte-equal to what `Build()` produces for the same inputs.
- `mercurius preview`'s output for an empty session matches broker round 1's prompt byte-for-byte except for the `SnapshotPath` sentinel.
- `mercurius preview` does not create any `.mercurius/` directory state.

## Sequence

M1 → M2 → M3 → M4 are roughly orthogonal; sequential matters mostly for clean commit history. M5 (preview) follows because its preview output should reflect any prompt changes from M3 (the swapped `fixed` prior-decisions text). M6 (docs) follows the code. M7 is housekeeping after M1-M6 land.

## Out of scope

- Per-round prompt overrides on `start_review_round` (separate later bundle).
- Web monitor / trajectory analytics (see `docs/future/web-monitor-and-trajectory.md`).
- Stretch items: prompt-effect tracking, multi-variant fix proposals.
- `posture` field as structured config.
- An MCP-tool surface for `preview` (CLI only in v1).
