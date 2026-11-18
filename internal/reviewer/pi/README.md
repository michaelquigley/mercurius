# pi Reviewer

This package implements `reviewer.Reviewer` for the [pi](https://pi.dev) coding agent.

How it is invoked, which flags it passes, and how its output is handled are documented in [Architecture and storage](../../../docs/current/architecture.md), under Reviewer Implementations. That page is the reference; this file does not restate it.

## Integration Test

Live pi coverage is gated behind the `integration` build tag:

```bash
go test -tags integration ./internal/reviewer/pi
```

Environment variables:

- `MERCURIUS_PI_BINARY` - optional pi binary path; defaults to `pi` on `PATH`.
- `MERCURIUS_PI_MODEL` - optional model in `provider/model` form; defaults to `openai-codex/gpt-6.1-sol`.
