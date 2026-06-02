# Claude Reviewer

The Claude reviewer implements `reviewer.Reviewer` by invoking the Claude Code CLI once per round in print mode. It does not assemble prompts, read artifacts, or validate schema output. The broker supplies the fully assembled prompt and schema, and the broker performs canonical schema validation after the reviewer returns.

## Invocation

For each `Review` call, the reviewer runs:

```bash
claude -p --output-format json --json-schema <schema> --permission-mode plan --no-session-persistence
```

The assembled `ReviewRequest.Prompt` is sent on stdin (it already inlines artifact content and the output schema, and can be large; stdin avoids argument-length limits). `ReviewRequest.WorkingDir` becomes the process working directory via `cmd.Dir`.

If `Options.Model` is set, `--model <model>` is appended (an alias such as `sonnet`/`opus`, or a full model name). `Options.ExtraArgs` are appended after the Mercurius-managed flags. `Options.BinaryPath` defaults to `claude`.

`--permission-mode plan` keeps the run read-only (claude can read but never writes). `--bare` is intentionally not set, so claude inherits the operator's logged-in subscription credentials. To use API-key-only auth instead, pass `--bare` via `extra_args` and set `ANTHROPIC_API_KEY` in the environment.

Because the working directory is the round's snapshot directory inside the project tree and `--bare` is not used, claude discovers and loads the project's `CLAUDE.md` by walking up from that directory; the review therefore carries whatever instructions that file holds. This matches codex (which loads `AGENTS.md`) and differs from the pi reviewer, which suppresses context files with `--no-context-files`. Passing `--bare` via `extra_args` also suppresses `CLAUDE.md` discovery. See `docs/current/architecture.md` for the cross-reviewer comparison.

## Output Handling

The reviewer reads claude's single `--output-format json` envelope from stdout. With `--json-schema`, the schema-validated object lands in the envelope's `structured_output` field, which the reviewer returns as `ReviewResponse.Raw`. If `structured_output` is absent (for example when the structured-output pass fails), the reviewer falls back to extracting the first JSON object from the envelope's `result` text. `UsageNotes` records the binary, model, subtype, cost, turn count, duration, and session id.

If the envelope reports `is_error` (for example a logged-out operator yields `"Not logged in"`), the reviewer surfaces that message as the error. If stdout is not a JSON envelope and the process also failed, the captured stdout and stderr are included in the error. It does not validate the object against the Mercurius review schema.

## Integration Test

Live Claude coverage is gated behind the `integration` build tag:

```bash
go test -tags integration ./internal/reviewer/claude
```

Environment variables:

- `MERCURIUS_CLAUDE_BINARY` - optional Claude binary path; defaults to `claude` on `PATH`.
- `MERCURIUS_CLAUDE_MODEL` - optional model passed as `--model <model>`.
