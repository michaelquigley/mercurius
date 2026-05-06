# Schema simplification: drop ready_to_ship in favor of verdict

The reviewer output schema currently carries two top-level fields that always agree by construction:

- `ready_to_ship`: boolean
- `verdict`: enum with values `ready_to_build`, `needs_changes`, `needs_discussion`

The cross-field readiness consistency rule binds them:

- `ready_to_ship: true` ⟺ `verdict == "ready_to_build"` ⟺ `concerns: [] && questions: []`
- `ready_to_ship: false` ⟺ `verdict ∈ {"needs_changes", "needs_discussion"}` ⟺ at least one concern or question

The boolean is fully determined by the verdict (and vice versa). The two fields carry the same information in two shapes; the cross-field check exists only to enforce that they agree.

This was surfaced during the prompt-philosophy bundle review (session `s_DZFWnoJyUNmF`, round 2) when the same readiness state was being referenced under two different names within a single round log.

## Proposed resolution

Drop `ready_to_ship`. Keep `verdict` as the single readiness signal. The readiness consistency check collapses from "ready_to_ship ⟷ verdict ⟷ concerns/questions" to just "verdict ⟷ concerns/questions": a `ready_to_build` verdict requires empty `concerns` and `questions`; any other verdict requires at least one entry.

Naming stays consistent. `ready_to_build` is the verdict for ready artifacts; `needs_changes` and `needs_discussion` are the unready verdicts. This matches the existing `close_session` verdict vocabulary (modulo `paused` and `abandoned`, which are session-level rather than round-level).

## Files affected

- `internal/schema/reviewOutput.go` — drop the `ReadyToShip` field.
- `internal/schema/reviewOutputSchema.json` — drop the `ready_to_ship` property and required entry.
- `internal/schema/reviewOutput_test.go` — fixture updates; remove cross-field readiness assertions.
- `internal/prompt/prompt.go` — verdict-and-severity section gets shorter; the locked-equivalence paragraph between `ready_to_ship` and `verdict` goes away.
- `internal/prompt/prompt_test.go` — assertion updates.
- `internal/reviewer/dummy/` — fixture no longer emits `ready_to_ship`.
- `internal/broker/broker.go` — readiness consistency check simplifies.
- `internal/broker/broker_test.go` — fixture and assertion updates.
- `docs/current/reviewer-output.md` — drop the `ready_to_ship` field description; tighten the readiness rules section accordingly.

## Why this was not folded into the prompt-philosophy bundle

The observation surfaced mid-review of an unrelated bundle that was already converging. Folding new schema scope at that point would have required another round to validate and would have muddied the bundle's clean scope ("prompt philosophy and visibility"). Splitting keeps both bundles tight.

Ordering this after the prompt-philosophy bundle is natural because the prompt logging and `_prompt.md` artifact let the new (verdict-only) readiness rule be observed in the assembled prompt directly, and the `docs/current/reviewer-output.md` rewrite can land alongside the prompt-section updates from the prompt-philosophy bundle. There is no hard dependency in either direction.

## Open questions

- Does any external consumer care about `ready_to_ship`? Current reviewer implementations are codex (subprocess JSON) and dummy (in-process); neither is third-party. The MCP tool surface returns reviewer output to the design agent, so design agents that branch on `ready_to_ship` rather than `verdict` would also need updating, but that is in-house too.
- Should the prompt's verdict-and-severity section gain a small example showing the verdict-only readiness mapping, or is removal of the boolean and its locked-equivalence paragraph sufficient on its own?
