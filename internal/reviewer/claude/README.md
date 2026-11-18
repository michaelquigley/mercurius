# Claude Reviewer

This package implements `reviewer.Reviewer` for the Claude Code CLI.

How it is invoked, which flags it passes, and how its output is handled are documented in [Architecture and storage](../../../docs/current/architecture.md), under Reviewer Implementations. That page is the reference; this file does not restate it.

## Integration Test

Live Claude coverage is gated behind the `integration` build tag:

```bash
go test -tags integration ./internal/reviewer/claude
```

Environment variables:

- `MERCURIUS_CLAUDE_BINARY` - optional Claude binary path; defaults to `claude` on `PATH`.
- `MERCURIUS_CLAUDE_MODEL` - optional model passed as `--model <model>`.
