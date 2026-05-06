# Mercurius — feedback after extended use

A practice-oriented dump from working through six-plus rounds across two sessions on the agora dashboard documents (~440KB combined, three artifacts). This isn't a design proposal — it's a notebook entry: what worked, what created friction, and what would unlock more iteration. Written by Claude on May 5, 2026, after working through the arc with Michael; intended as input for mercurius development.

## Context

Two sessions back-to-back on the same artifact set:

- Session 1 (`s_PdAYiFYOJER9`): 4 rounds, full budget consumed, 11 blocking findings resolved across the arc, closed `ready_to_build`.
- Session 2 (`s_FrkQpPqOmteZ`): cold-start session against the post-session-1 docs. Round 1 surfaced 3 new blocking findings (none recurrences from session 1's arc), round 2 surfaced 3 more.

Reviewer was codex (gpt-5.5) throughout, with `default_budget: 4` and `max_findings: 6`. Cadence was one-finding-per-turn. Disposition recording via `record_round_notes` after each finding.

## What worked well

**The blocking-vs-advisory split.** Added mid-session 1. Immediately changed how I triaged: before, every finding had to be read fully before deciding whether to act; after, I could pre-sort by severity and put attention on blocking concerns first. Advisory notes became polish to consider after the blocking work was done. This is a high-value structural choice.

**The triage block with `next_finding` and per-turn `next_action` guidance.** The "pause and ask the user before addressing other findings" instruction was actually load-bearing for keeping the cadence sensible. Without it, the natural pull is to batch-process findings, which loses per-finding decision quality.

**Disposition discipline.** The accepted/rejected/deferred enum forces a clean decision per finding. The rejection of "fixed" as a disposition is a small surprise (see friction below) but the underlying constraint — *make a decision, don't just describe what you did* — is correct. The decision log accumulates real audit value.

**Verdict enum at close (`ready_to_build` / `paused` / `abandoned`).** Forces decisive close-out. I noticed myself wanting to write essay-form summaries; that constraint is doing real work.

**Per-round snapshot logging.** The `.mercurius/{session_id}/snapshots/` tree preserved exactly what codex saw on each round, which made it possible to reason about what changed between rounds. State preservation at this granularity is what makes the practice feel substantial rather than ephemeral.

**The convergence block in `collect_round`.** Useful even when it returns "signal: none" — it surfaces a comparison framework. See friction notes for what would make it richer.

## What created friction

### Codex's quota-shaped attentional pattern

This is the biggest meta-issue. Codex optimizes for "find max_findings of varying severity," not "is this doc ready?" Even on docs codex itself characterizes as "implementation-ready," it returns 3-6 findings every round. Trajectory matters more than the count: round 1 of session 1 was DB topology gaps; round 4 was wording bugs; round 1 of session 2 (cold start) was schema scoping. That's real convergence at constant count. But the tool doesn't surface the trajectory — the user has to derive it by comparing rounds.

This is downstream of prompting. The current `prompt_overrides` (before today's rewrite) was a checklist: "contradictions, missing acceptance criteria, durability risks, CLI/API compatibility, data-loss, test gaps, ordering hazards." That's an enumerated prompt to find things in those categories, not a filter to surface only material things. The rewrite I just landed in mercurius.yaml replaces the checklist with an obvious-vs-subtle filter and explicit permission to emit fewer than `max_findings`. Whether it works is the next question to test — and the inability to test it without burning a real round is itself feedback (see below).

### No assembled-prompt visibility

Through the entire arc I rewrote prompt extensions based on inference about what codex actually sees. The `review_context` and `prompt_overrides` blocks get assembled with mercurius's internal base prompt; I can guess at what that final prompt looks like, but I can't read it. When I rewrote `prompt_overrides` today, I was guessing about whether the two blocks get equivalent weight, whether the base prompt itself biases codex toward checklist behavior (which would partially neutralize my anti-checklist guidance), and whether codex is even told about `max_findings` directly (which would conflict with the "fewer findings is OK" line I added).

This is the highest-leverage tooling gap and is addressable cheaply: log the assembled prompt to disk per round, e.g. `.mercurius/{session_id}/round-{N}/prompt.txt`. Once that exists, every other prompt-engineering improvement gets faster, because the iteration loop becomes "edit, run, read what was sent" rather than "edit, run, infer from results."

### Single `prompt_overrides` block for multi-aspect guidance

My rewrite of `prompt_overrides` had to fit "what to flag," "what not to flag," "fix-size discipline," and "permission to emit fewer findings" into one block. Each is independently tunable, but the single-block structure means changing one means re-touching the others. Splitting into named fields — e.g. `review_lens`, `findings_filter`, `output_constraints`, `fix_sizing` — would let me iterate on one aspect at a time without disturbing the others.

### Advisory dispositions can't be formally recorded

`record_round_notes` only accepts refs from `triage.findings` (concerns/questions), not advisory_notes. When I wanted to record dispositions for advisory notes (e.g., "deferred because already addressed elsewhere in prose," or "accepted with two-edit fix"), the tool rejected it with `unknown_ref`. The result: the docs themselves become the record for advisory disposition, not the session log. Either fix the asymmetry (allow advisory refs even if the disposition vocabulary is different) or document it as intentional in the protocol.

### Disposition vocabulary

"Fixed" isn't a valid disposition value. I had to map "edited the docs, the issue is now resolved" to "accepted" repeatedly. The natural-language mismatch is mild but persistent — "accepted" reads as "we agree with the finding," not "we addressed it." Either rename "accepted" to something less ambiguous or expand the vocabulary to include "fixed."

### No prompt-iteration loop

Every prompt change requires kicking off a real round (1-2 minutes) to test, and the signal is noisy: was the difference because the prompt changed, or because the doc state changed since the previous round, or because codex's sampling temperature varied? Hard to isolate prompt-change effects without a faster iteration mechanism — either dry-run / preview, or the ability to re-run a round against an unchanged snapshot with a modified prompt.

### No way to A/B prompt variants in a session

`open_session` accepts a `review_context` override but not a `prompt_overrides` override. Symmetric override would let me experiment with prompt variants in a single session without committing to mercurius.yaml first.

### Codex's suggested fixes are systematically over-engineered

This is a behavioral observation, not strictly a tool issue, but it's visible through the tool. Every round had at least one finding where codex suggested the production-grade fix (admin actor model, full cross-repo YAML schema coordination, endpoint-specific schemas everywhere, synthetic-row markers for idempotency) and the MVP-correct fix was meaningfully smaller. The user has to apply the MVP-discipline lens manually each time. Today's `prompt_overrides` rewrite tries to push codex toward smaller fixes; whether that works needs testing.

A possible structural fix: have codex return 2-3 fix variants per finding at different sizes ("smallest," "thorough," "production-grade") and let the user pick. Costs more reviewer tokens; saves user attention.

### `next_finding` selection by ID order, not by severity

When the round returns 1 blocker + 2 major, the user typically wants to address the blocker first; the tool defaults to `next_finding = C-001` regardless of where the blocker sits in ID order. Easy to override but counts as friction. Default to highest severity remaining.

### Filesystem-tool footguns observed while writing this very document

Three small ergonomic gaps surfaced while trying to write `feedback.md` itself, each worth flagging:

1. **The native `create_file` and the `filesystem:create_file` MCP share a path namespace but write to different filesystems.** The native tool reports "File created successfully" with the host-shaped path even when the file lands in Claude's container instead of on the host. This is a silent-failure footgun — the natural caught-by-error mechanism that exists for `view`/`str_replace` (which fail loudly when pointed at host paths from the native tool) doesn't exist for `create_file`. Mitigation is ideally at the agent harness layer, but mercurius could help by having its filesystem-touching tools' path conventions be visibly distinct from generic filesystem paths.

2. **The currently-loaded filesystem MCP toolset in this session has no `create_file` primitive.** It has `copy`, `grep`, `list_directory`, `move`, `read_file`, `read_files`, `replace_lines`, `str_replace` — all of which assume the target file already exists. The `tool_search` capability for loading deferred tools (which would have included `filesystem:create_file`) also wasn't exposed. So once I knew the native tool was wrong, I had no in-session path to write a new file directly. Worked around by having the user create the file manually, then copying an existing file's contents over it via `filesystem:copy`, then replacing the contents via `replace_lines`. The right fix is to expose `filesystem:create_file` (or `write_file`) as a default-loaded tool — not to require coreography of three operations.

3. **`replace_lines` on an empty (0-line) file rejects `start_line=1` with "exceeds file length 0," and `start_line=0` with "invalid line range 0-0."** There's no way to populate an empty file using the loaded toolset; the file must have at least one line first. Combined with #2, this means a freshly-created empty file is a dead-end. Either let `replace_lines` accept an empty range that means "insert," or let `str_replace` accept `old=""` for empty-file initialization.

These are minor compared to the prompt-engineering gaps, but they bit immediately on the first concrete attempt to use the tools for something other than editing existing files. Worth flagging because they belong to the same category of "tool-side asymmetries the user has to work around" as the advisory-disposition issue earlier.

## Practice insights worth capturing

**Trajectory matters more than per-round counts.** The convergence signal isn't whether codex returns zero findings (probably unreachable on docs of this size); it's whether the severity character of findings drops over time. Round 1's "DB topology undefined" → round 4's "wording bug" is real convergence at constant count. The tool could surface this directly: instead of `signal: none`, emit something like `severity_skew: -1.5 over 4 rounds` or `domain_shift: architectural → spec_polish` or similar. Right now the user has to do this comparison manually.

**Cold-start sessions are a structurally different review, not a redundant one.** The continued session has conversational momentum that biases toward dimensions it has already explored; the fresh session sees the docs cold and finds different angles. Tested empirically across the two sessions: round 1 of `s_FrkQpPqOmteZ` surfaced three findings (catalog display data, count_delta computation, contract source-of-truth) that the four-round arc of the prior session had not produced. Not noise — genuinely different surface area exposed.

The pattern that emerged: **continued sessions for tight iteration on a finding's domain; fresh sessions for cold-eyes architectural review.** Worth documenting as recommended practice. Maybe even: after N rounds in a continued session without convergence signal, prompt the user to consider a fresh-session pass.

**The MVP-discipline lens is human value-add.** Through the entire arc, the user supplied the "is this worth it?" judgment that codex couldn't. Codex surfaces concerns; the human triages by relevance. This asymmetry is structural and the tool could expose it more deliberately — maybe a `posture` field at session-open ("MVP" / "production-grade" / "exploratory") that codex actually internalizes via its prompt extension, or a per-finding "is this load-bearing?" filter the user applies post-hoc.

**Convergence is asymptotic, possibly unreachable.** Each round draws findings from a different part of a 440KB doc surface. The right close-out criterion isn't "zero findings" but "the findings stop being load-bearing for implementation." This needs to be in the protocol explicitly, otherwise users (and Claude-the-collaborator) chase a thumbs-up that isn't coming. Mid-arc, Michael said "I keep hoping we'll get a thumbs up from the implementing agent, but that might just be a pipe dream." That's exactly the trap, and naming it would help.

**Per-finding-per-turn cadence works.** Forces engagement with each finding individually rather than batch-processing. Costs latency but improves decision quality measurably — several findings had over-engineered defaults that only got caught because the user had time to think between findings.

**The prompt is the lever.** The right level to influence codex's review behavior is the prompt extensions, not the user's after-the-fact triage. We discovered this when we updated mercurius.yaml mid-arc. The user-side burden of triage is downstream of how aggressively codex surfaces non-material findings, which is downstream of the prompt.

## Tooling proposals, ranked by value-per-effort

1. **Log assembled prompts to disk per round.** Cheapest, highest value. `.mercurius/{session_id}/round-{N}/prompt.txt` (or `.json` if structured). Unlocks every other prompt-engineering improvement because the user can finally see what codex actually saw.

2. **Decompose `prompt_overrides` into named sections.** Suggested split: `review_lens` (filter principle), `findings_filter` (do/don't flag rules), `output_constraints` (count and fix-size rules), `fix_sizing` (anti-over-engineering directive). Lets users iterate on one aspect at a time.

3. **Per-session `prompt_overrides` override.** Symmetric to the existing `review_context` override on `open_session`. Enables single-session A/B experiments without committing variants to mercurius.yaml.

4. **Prompt preview / dry-run.** Either a `mercurius:preview_prompt` MCP tool that returns the assembled prompt for a hypothetical round, or a `dry_run: true` flag on `start_review_round`. Cuts the prompt-iteration loop in half — prompts can be tested without round latency.

5. **Allow advisory refs in `record_round_notes`.** Or document the asymmetry explicitly in the protocol. Either path beats the current silent rejection (`unknown_ref`).

6. **Expanded disposition vocabulary.** Add `fixed` (or rename `accepted`). Small ergonomic win that aligns the tool's vocabulary with the natural way users describe what they did.

7. **Richer convergence signal.** Beyond "signal: none" — surface trajectory observations like severity-skew, domain-shift, recurrence-rate. Helps users recognize asymptotic convergence without manual cross-round comparison.

8. **`next_finding` selection by severity, not ID order.** Default to highest severity remaining. Easy to override if the user wants something else.

9. **Expose `filesystem:create_file` as a default-loaded tool** (or fix `replace_lines` / `str_replace` to accept empty-file initialization). The current "no in-session path to populate a new file" is a real workflow break.

10. **(Stretch) Prompt-effect tracking.** When prompts change between rounds, mercurius could note `prompt_changed: true` in round metadata along with a content hash. Helps users correlate prompt iterations with finding distribution changes over many rounds.

11. **(Stretch) Multiple fix proposals at different sizes.** Each finding could include 2-3 fix variants. Lets users pick the size-appropriate one without re-prompting codex. Costs more reviewer tokens; saves user attention.

## Closing: "when are review docs done?"

This was the implicit question through the whole arc. The honest answer that emerged: **not when codex returns zero findings, but when the findings codex returns stop being load-bearing for the implementer the docs are written for.**

The mercurius protocol could state this explicitly somewhere — maybe in close_session's verdict semantics ("ready_to_build" doesn't mean "no findings remain"; it means "remaining findings are noise-floor against the user's quality bar") — or leave it for users to derive. Either way, naming it would help users avoid the "thumbs-up that isn't coming" trap that we both noticed mid-arc.
