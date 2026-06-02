# Settled-Decisions Follow-Ups

The settled-decisions feature shipped on 2026-06-01; its current behavior is documented in `docs/current/` (configuration, architecture, the agent guide). Guard entries are deliberately minimal — each is `{id, do_not_flag}` and nothing more. One refinement was considered during that work and deliberately deferred. It is recorded here so the trigger to revisit it is not lost. Nothing below is implemented.

## A richer entry shape

`settled_decisions` entries could carry `decided` and `reason` fields alongside `do_not_flag` — recording *what* was decided and *why*, not just what to stop flagging. A richer entry would help a fresh reviewer distinguish a settled concern from a genuinely-new adjacent one, rather than going blind to a whole neighborhood.

It was rejected for now on bloat grounds: the field's worst failure mode is a ledger that grows faster than the artifacts it guards — guards added speculatively, reasons written at length — slowing every cold read without buying anything. The minimal entry is the defense against that, and a guard should cost almost nothing to write so that undoing a bad one costs almost nothing too.

The trigger to revisit: if real use shows the minimal entry causes the reviewer to *over-suppress* — going blind to adjacent concerns it should still raise — that is the signal the extra structure is earning its weight. Until that shows up in practice, minimal holds.

## Related

- `docs/current/configuration.md` — the shipped `settled_decisions` field and its `{id, do_not_flag}` shape.
- `review-round-modes.md` — the fresh-reader round, which suppresses these guards for a single round.
