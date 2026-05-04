# Mercurius

Workflow broker for design-agent review loops. Mercurius runs as a local MCP server, snapshots design artifacts, dispatches them to a configured reviewer, validates structured review output, and writes durable round logs.

## project context

See `docs/design.md` for the product and architecture design. See `docs/work-order.md` for the milestone implementation plan.

M1 establishes the Go scaffold, reviewer interface, structured review schema, schema validation, and a dummy reviewer for tests. Later milestones add Codex subprocess execution, broker/session orchestration, prompt assembly, log writing, configuration, and MCP tools.

## tech stack

- **language**: Go 1.26+
- **CLI**: github.com/spf13/cobra, starting in M4
- **config**: github.com/michaelquigley/df/dd, starting in M4
- **logging**: github.com/michaelquigley/df/dl
- **MCP**: github.com/modelcontextprotocol/go-sdk/mcp, starting in M4
- **schema validation**: github.com/santhosh-tekuri/jsonschema/v6
- **build/test**: standard `go build`, `go vet`, and `go test`

## package structure

- `cmd/mercurius/` - binary entrypoint
- `internal/reviewer/` - Reviewer interface and shared request/response types
- `internal/reviewer/codex/` - Codex subprocess reviewer implementation
- `internal/reviewer/dummy/` - in-process reviewer for tests
- `internal/schema/` - structured review output JSON Schema and validation
- `docs/` - design and work-order documents
- `README.md` - user-facing overview

Planned packages for later milestones include `internal/broker`, `internal/prompt`, `internal/roundlog`, and `internal/config`.

## project rules

1. in Go code, all comments start with a lowercase letter, unless the first word refers to a Go type that starts with an uppercase letter.

2. all outputs logged or otherwise emitted to a user prefer lowercase, unless they reference a type that requires uppercase letters. dynamic data in outputs appears between single quotes, like "the session 's_xK3p9q' was closed".

3. Go files are named like `dashManager.go`, not `dash_manager.go`. unit tests are named `dashManager_test.go`.

4. never use emoji in code, comments, or output.

5. clean up build artifacts, binaries, and test executables created during development or testing. do not leave compiled binaries in the repository.

6. use mermaid diagrams in markdown documents instead of ASCII art.

7. `dd` struct tags are only needed for `+required`, `+extra`, or name overrides. `dd` converts CamelCase fields to `snake_case` YAML keys automatically.
