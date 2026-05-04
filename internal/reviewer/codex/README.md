# Codex Reviewer

The Codex reviewer implements `reviewer.Reviewer` by invoking the local Codex CLI once per round. It does not assemble prompts, read artifacts, or validate schema output. The broker supplies the fully assembled prompt and schema, and the broker performs canonical schema validation after the reviewer returns.

## Invocation

For each `Review` call, the reviewer runs:

```bash
codex exec -C <session_dir> --ephemeral --sandbox read-only --output-schema <tmp>/schema.json --output-last-message <tmp>/last-message.json
```

If `Options.Model` is set, `-m <model>` is appended. `Options.ExtraArgs` are appended after Mercurius-managed flags and are intended for non-conflicting Codex CLI options.

`Options.BinaryPath` defaults to `codex`. `Options.WorkingDir` is required and should be the Mercurius session log directory. That directory becomes the Codex working root via `-C`.

## Temp Files

Each review creates a private temporary directory containing:

- `schema.json` - the exact `ReviewRequest.Schema` bytes passed via `--output-schema`.
- `last-message.json` - the file Codex writes via `--output-last-message`.

The assembled `ReviewRequest.Prompt` is sent on stdin. The temp directory is removed before `Review` returns, on both success and failure.

## Output Handling

The reviewer reads `last-message.json`, strips conventional wrappers when necessary, and returns the first plausible JSON object as `ReviewResponse.Raw`. It accepts direct JSON, fenced JSON, or JSON surrounded by stray prose. It does not validate the object against the Mercurius review schema.

If Codex exits unsuccessfully, the reviewer returns an error that includes captured stdout and stderr. If Codex succeeds but no JSON object can be extracted, the reviewer returns a normal reviewer error.

## Integration Test

Live Codex coverage is gated behind the `integration` build tag:

```bash
go test -tags integration ./internal/reviewer/codex
```

Environment variables:

- `MERCURIUS_CODEX_BINARY` - optional Codex binary path; defaults to `codex` on `PATH`.
- `MERCURIUS_CODEX_MODEL` - optional model passed as `-m <model>`.
