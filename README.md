# Mercurius

_Mediates the design-build duel._

Mercurius is a workflow broker that sits between a design agent and an implementing agent during the iterative review of design documents and work orders.

## The problem

A common pattern when building software with AI collaborators:

1. A design agent helps you think through and document a piece of work.
2. An implementing agent reviews the design before building, and surfaces concerns.
3. You shovel feedback between the two across rounds until the design is buildable.

Step 3 is the tax. Moving feedback from one agent to another is mechanical work, and the human in the loop ends up spending attention on transport when it should be reserved for decisions.

## What Mercurius does

Mercurius runs as an MCP server that the design agent can call. Each call:

1. Runs the implementing agent against the current design artifacts under a constrained review prompt.
2. Returns structured output — verdict, concerns, questions, optional concrete diffs — rather than free-form prose.
3. Logs the round to a configurable destination so the audit trail accumulates without effort.

The design agent then triages the output, applies what is mechanical, and surfaces the actual decisions to the human. The loop continues until the verdict is `ready_to_build` or the human calls it.

Mercurius is reviewer-agnostic by design. Codex is the first implementation; the interface is built so other implementing agents can be swapped in without touching the orchestration layer.

## Status

Design phase. The repository contains the vision, the design, and the work order. No implementation yet.

- [`docs/design.md`](docs/design.md) — what Mercurius is and how it works
- [`docs/work-order.md`](docs/work-order.md) — the implementation plan

## License

Apache 2.0. See [`LICENSE`](LICENSE).
