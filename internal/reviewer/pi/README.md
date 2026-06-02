# pi Reviewer

The pi reviewer implements `reviewer.Reviewer` by invoking the [pi](https://pi.dev) coding agent once per round in print mode. It does not assemble prompts, read artifacts, or validate schema output. The broker supplies the fully assembled prompt and schema, and the broker performs canonical schema validation after the reviewer returns.

## Invocation

For each `Review` call, the reviewer runs (flags verified against pi v0.78.0):

```bash
pi -p --mode json --no-session --no-context-files --tools read,grep,find,ls --model <provider/model> @<prompt-file>
```

The assembled `ReviewRequest.Prompt` is written to a temporary file and passed as an `@file` reference (pi reads the file content into the message). The prompt inlines artifact content and can be large, so a file reference avoids argument-length limits. The subprocess stdin is left closed (the null device): pi consumes stdin as additional input, and an open stdin with no EOF would hang the run. `ReviewRequest.WorkingDir` becomes the process working directory via `cmd.Dir`.

If `Options.Model` is set, `--model <provider/model>` is appended (pi uses the `provider/id` form, for example `openai-codex/gpt-5.5`). `Options.ExtraArgs` are appended after the Mercurius-managed flags and before the `@file`. `Options.BinaryPath` defaults to `pi`.

`--tools read,grep,find,ls` keeps the run read-only (no file modifications). `--no-session` makes the run ephemeral. `--no-context-files` suppresses `AGENTS.md`/`CLAUDE.md` discovery, which matters because the round directory sits inside the project tree; without it pi would fold the project's own agent instructions into the review. This differs from the codex and claude reviewers, which do load those context files; see `docs/current/architecture.md` for the cross-reviewer comparison.

## Schema Handling

Unlike codex and claude, pi has no native JSON-Schema enforcement. The schema is conveyed only by the prompt, which already inlines the output schema and a "respond with a single JSON object only" instruction. The reviewer therefore relies on the broker's post-return schema validation as the backstop.

## Output Handling

The reviewer reads pi's `--mode json` newline-delimited event stream from stdout and takes the last `message_end` event whose `message.role` is `assistant`, concatenating the `text` content blocks (ignoring `thinking` blocks). It then extracts the first JSON object from that text as `ReviewResponse.Raw`. If pi exits non-zero but produced a usable object, that object is returned; if no object can be recovered and the process failed, the captured stdout and stderr are included in the error. It does not validate the object against the Mercurius review schema.

## Integration Test

Live pi coverage is gated behind the `integration` build tag:

```bash
go test -tags integration ./internal/reviewer/pi
```

Environment variables:

- `MERCURIUS_PI_BINARY` - optional pi binary path; defaults to `pi` on `PATH`.
- `MERCURIUS_PI_MODEL` - optional model in `provider/model` form; defaults to `openai-codex/gpt-5.5`.
