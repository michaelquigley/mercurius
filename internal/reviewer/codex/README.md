# Codex Reviewer

This package implements `reviewer.Reviewer` for the Codex CLI.

How it is invoked, which flags it passes, and how its output is handled are documented in [Architecture and storage](../../../docs/current/architecture.md), under Reviewer Implementations. That page is the reference; this file does not restate it.

## Integration Test

Live Codex coverage is gated behind the `integration` build tag:

```bash
go test -tags integration ./internal/reviewer/codex
```

Environment variables:

- `MERCURIUS_CODEX_BINARY` - optional Codex binary path; defaults to `codex` on `PATH`.
- `MERCURIUS_CODEX_MODEL` - optional model passed as `-m <model>`.
