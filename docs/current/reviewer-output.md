# Reviewer Output Schema

Reviewer output is a single JSON object. Mercurius validates it against a strict JSON schema and then applies cross-field readiness checks.

## Shape

```json
{
  "verdict": "ready_to_build",
  "summary": "one-paragraph assessment",
  "concerns": [],
  "questions": [],
  "advisory_notes": [],
  "proposed_diffs": []
}
```

## Readiness Rules

`verdict: "ready_to_build"` requires:

- `concerns: []`
- `questions: []`

`verdict: "needs_changes"` or `verdict: "needs_discussion"` requires at least one entry in `concerns` or `questions`.

`advisory_notes` never block readiness regardless of verdict.

## Concerns

Concerns are blocking findings.

```json
{
  "id": "C-1",
  "severity": "major",
  "location": "product-design.md",
  "claim": "scope is ambiguous",
  "rationale": "two implementers could build materially different behavior",
  "suggestion": "make the acceptance criteria explicit"
}
```

Severity is one of:

- `blocker`: implementation cannot proceed without resolving this.
- `major`: implementation could proceed but would produce something materially different from intent.
- `minor`: low-impact readiness issue. If it is not readiness-blocking under the context, it belongs in `advisory_notes`.

## Questions

Questions are blocking clarifications.

```json
{
  "id": "Q-1",
  "topic": "rollout order",
  "why_it_blocks": "the implementation plan cannot be checked without migration ordering"
}
```

## Advisory Notes

Advisory notes are non-blocking polish or downstream considerations.

```json
{
  "id": "A-1",
  "location": "implementation-plan.md",
  "note": "shorten the test plan wording",
  "rationale": "the current wording is correct but harder to scan",
  "suggestion": "collapse the two bullets into one"
}
```

Advisory notes are returned to the design agent as `triage.advisory_notes`, separate from blocking findings. They do not count against `max_findings`. The design agent can disposition advisory ids via `record_round_notes` the same way it dispositions concerns and questions; advisory dispositions are recorded in the round log alongside blocking-finding dispositions, but they do not block readiness.

## Proposed Diffs

Proposed diffs are optional concrete edits.

```json
{
  "id": "D-1",
  "target": "implementation-plan.md M2",
  "patch": "concrete replacement text or unified diff"
}
```

A concern or question does not need a proposed diff. `proposed_diffs: []` is normal.

## Strictness

All top-level fields are required. Arrays must be present even when empty. Unknown fields are rejected at every object level. Empty strings are not accepted for nullable suggestion fields; use `null` when there is no suggestion.

Every id appearing in `concerns`, `questions`, or `advisory_notes` must be unique across all three arrays. An id may not be reused within a single array, and may not appear in more than one of those arrays. Reviewer output that violates this rule is rejected with `schema_violation`. JSON Schema cannot express cross-array uniqueness, so the reviewer prompt states the rule explicitly and the broker enforces it after parsing.
