# Reviewer Output Schema

Reviewer output is a single JSON object. Mercurius validates it against a strict JSON schema and then applies cross-field readiness checks.

## Shape

```json
{
  "ready_to_ship": true,
  "verdict": "ready_to_build",
  "summary": "one-paragraph assessment",
  "concerns": [],
  "questions": [],
  "advisory_notes": [],
  "proposed_diffs": []
}
```

## Readiness Rules

`ready_to_ship: true` requires:

- `verdict: "ready_to_build"`
- `concerns: []`
- `questions: []`

`ready_to_ship: false` requires:

- `verdict` is `needs_changes` or `needs_discussion`
- at least one entry in `concerns` or `questions`

`advisory_notes` never make a round unready.

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

Advisory notes are returned to the design agent as `triage.advisory_notes`, separate from blocking findings. They do not count against `max_findings`.

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
