# Future Calibration Ideas

The current implementation includes the highest-leverage calibration changes: review context, explicit `ready_to_ship`, advisory notes, decisions log carry-forward, finding caps, and convergence hints.

The ideas below are not implemented.

## Batch Judgment Mode

Current triage guidance asks the design agent to present all blocking findings, then address one finding per user turn. A future batch mode could let the human classify the full finding set at once, especially in late rounds where many findings are small.

Possible shape:

- `collect_round` includes a `triage.mode` option or a separate batch-triage hint.
- The design agent asks which findings matter as a set.
- Notes can record decisions for many refs in one call.

Risk: batch mode can encourage skipping discussion of a subtle blocker.

## More Nuanced Session Verdicts

Current close verdicts are `ready_to_build`, `paused`, and `abandoned`.

A future verdict such as `ready_to_build_with_intentional_declines` could make audit trails clearer when the human explicitly rejects or defers reviewer findings under the stated context.

Open question: this may be better represented by `decisions.md` plus `ready_to_build`, without adding another verdict.

## Context-Aware Default Budget

Current `default_budget` is static. A future heuristic could suggest or set a budget based on artifact size, review context, and stakes.

Examples:

- Short personal-tool specs default to 2 rounds.
- High-stakes production designs default to 4 or 5 rounds.
- Very large artifact sets warn before opening a low-budget session.

Open question: whether this should be advisory only or actually alter budget.

## Multi-Reviewer Agreement Signals

If panel mode lands, Mercurius could compute agreement signals:

- same concern raised by multiple reviewers
- reviewer disagreement on readiness
- one-reviewer-only findings likely to be advisory

This requires finding similarity, not just exact ref matching, so it should follow real panel-mode usage rather than precede it.
