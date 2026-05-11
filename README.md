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

Mercurius runs as an MCP server that the design agent can call. The design agent opens a session (a light grouping of related rounds), then for each review:

1. Runs the implementing agent against the supplied artifacts under a constrained review prompt.
2. Returns structured output — readiness verdict, blocking concerns/questions, advisory notes, optional concrete diffs — rather than free-form prose.
3. Logs the round to a self-contained per-round directory so the audit trail accumulates without effort.

The design agent then walks the blocking findings one at a time, explaining each finding and its proposed solution clearly and briefly, discussing it with the human, and implementing the fix once aligned. Advisory notes are presented separately as non-blocking polish. The loop continues until the verdict is `ready_to_build` or the human calls it.

Rounds are single-shot and self-contained: artifacts and findings are scoped to one round; nothing carries forward between rounds in the same session. The common workflow is `open session → review → fix → close → repeat`.

Mercurius is reviewer-agnostic by design. Codex is the first implementation; the interface is built so other implementing agents can be swapped in without touching the orchestration layer.

## Status

Mercurius has a working local MCP server, Codex and dummy reviewers, background single-shot review rounds, structured output validation, per-round logs, and CLI monitoring.

- [`docs/current/`](docs/current/README.md) — documentation for the current implementation
- [`docs/current/user-guide.md`](docs/current/user-guide.md) — real-world usage guide
- [`docs/future/`](docs/future/) — future changes and deferred designs
- [`examples/mercurius.yaml`](examples/mercurius.yaml) — starter project config

## Install

From this checkout:

```sh
go install ./cmd/mercurius
```

Then run the MCP server on stdio:

```sh
mercurius --config /absolute/path/to/mercurius.yaml
```

Logs go to stderr because stdout is reserved for MCP transport messages.

## Configuration

Minimal config:

```yaml
max_findings: 6
review_context: |
  Add project constraints that should calibrate reviewer rigor.
review_focus: |
  Add project-specific things to look for that the base review philosophy
  does not already cover.
reviewers:
  - name: codex
    impl: codex
    model: gpt-5.5
```

`review_context` and `review_focus` are read from this file; they are not MCP tool inputs. Edit the YAML before opening a session if you want different calibration. Relative `log_destination` and `binary_path` values resolve relative to the config file, not the process working directory.

## MCP Usage

Example MCP client configuration:

```json
{
  "mcpServers": {
    "mercurius": {
      "command": "mercurius",
      "args": ["--config", "/absolute/path/to/mercurius.yaml"]
    }
  }
}
```

Tools exposed:

- `open_session`
- `start_review_round`
- `round_status`
- `collect_round`
- `record_round_notes`
- `close_session`
- `session_status`
- `list_reviewers`
- `list_sessions`

Manual smoke path: connect an MCP client, call `open_session` (no artifacts at this stage), call `start_review_round` with absolute artifact paths for the design/work-order files you want reviewed, monitor it with:

```sh
mercurius monitor --config /absolute/path/to/mercurius.yaml --session <session_id> --wait
```

When the round completes, call `collect_round`, walk `triage.findings` one at a time with `triage.advisory_notes` separate, record commentary/decisions with `record_round_notes`, then either start another round in the same session, open a new session, or call `close_session`.

## License

Apache 2.0. See [`LICENSE`](LICENSE).
