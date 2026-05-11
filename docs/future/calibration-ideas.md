# Future Calibration Ideas

The current implementation includes the highest-leverage calibration changes: review context, project-specific focus, explicit `verdict`, advisory notes, and finding caps. Sessions are light groupings of single-shot rounds.

The ideas below are not implemented.

## Batch Judgment Mode

Current triage guidance asks the design agent to walk findings one at a time. A future batch mode could let the human classify the full finding set at once, especially when many findings are small.

Possible shape:

- `collect_round` includes a `triage.mode` option or a separate batch-triage hint.
- The design agent asks which findings matter as a set.
- Notes can record decisions for many refs in one call.

Risk: batch mode can encourage skipping discussion of a subtle blocker.

## Multi-Reviewer Agreement Signals

If panel mode lands, Mercurius could compute agreement signals:

- same concern raised by multiple reviewers
- reviewer disagreement on readiness
- one-reviewer-only findings likely to be advisory

This requires finding similarity, not just exact ref matching, so it should follow real panel-mode usage rather than precede it.

## Cross-Round Trajectory Analytics

When a session accumulates multiple rounds, an out-of-band tool (the future web monitor, or a CLI report) could compute trajectory observations - severity skew, domain shift, recurrence rate - across the round logs without coupling them via shared mutable state. The new round model preserves this option: rounds are still ordered and queryable from disk, they just don't carry state forward at build time.
