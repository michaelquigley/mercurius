# Configuration

Mercurius is configured by one YAML file per running server. By default the CLI loads `./mercurius.yaml`; pass another path with `--config`.

## Minimal Config

```yaml
reviewer:
  name: codex
  impl: codex
```

## Full Example

```yaml
log_destination: ./reviews
max_findings: 8
review_context: |
  Deployment: personal tool, single supervised implementer.
  Preference: simple over highly defensive when both achieve correctness.
settled_decisions:
  - id: observability-out-of-scope
    do_not_flag: missing production-grade observability or multi-tenant concerns
review_focus: |
  Pay particular attention to invariants specific to this project that the
  universal what-to-flag criteria do not already cover.
reviewer:
  name: codex
  impl: codex
  binary_path: /usr/local/bin/codex
  model: gpt-5.5
  extra_args:
    - --some-flag
```

## Required Fields

- `reviewer.name`: unique reviewer name (free-form).
- `reviewer.impl`: reviewer implementation. Current implementations are `codex` and `dummy`.

## Optional Fields

- `log_destination`: directory where session logs, status files, and snapshots are written. Default is `.mercurius` (relative to the config file's directory).
- `max_findings`: maximum blocking findings across `concerns` plus `questions` in a successful round. Default is `6`. Advisory notes do not count.
- `review_context`: free-form markdown calibration - the stable framing of what kind of review this is (deployment model, stakes, scope, simplicity-vs-defensiveness preference). It does not hold guards; decisions the reviewer should stop raising go in `settled_decisions`.
- `settled_decisions`: a list of guards - decisions already made that the reviewer should not re-raise. Each entry is `{id, do_not_flag}`: `do_not_flag` is the instruction rendered into the prompt as its own block; `id` is an operator-side handle for finding, editing, or removing the guard and is never shown to the reviewer. Entries whose `do_not_flag` is empty are ignored, and duplicate ids are tolerated. Earn guards by re-litigating a finding and deciding it is out of scope - do not add them speculatively, since a ledger that outgrows the artifacts slows every cold read.
- `review_focus`: free-form markdown for project-specific things to look for that the base review philosophy does not already cover (typically one paragraph). Inserted in the project-specific focus section of the prompt.
- `reviewer.binary_path`: reviewer binary path. For `codex`, omitted means use normal executable lookup.
- `reviewer.model`: reviewer model string passed to the reviewer implementation.
- `reviewer.extra_args`: additional reviewer-specific CLI arguments.

`review_context`, `review_focus`, and `settled_decisions` are configuration-only - they are not MCP tool inputs. Mercurius re-reads `mercurius.yaml` at the start of every round, so editing them between rounds takes effect on the very next round with no session reopen. (Hand-editing `settled_decisions` between rounds is therefore the way to amend the settled context mid-arc; there is no separate amend tool.) A broken or unreadable YAML fails `start_review_round` with a `user_error` naming the problem and leaves the session active, so a fixed file simply succeeds on the next try.

## Removed Fields

The config loader rejects configs that still include these keys with a clear error:

- `default_budget`: rounds are single-shot and no longer share a budget across a session.
- `prompt_overrides`: renamed to `review_focus`.
- `reviewers` (array): renamed to `reviewer` (singular). Mercurius runs exactly one reviewer per session; multi-reviewer panel mode is future work.

## Project Name

The project name is derived from the basename of the directory containing the config file (e.g., a config at `~/work/archive/mercurius.yaml` produces project name `archive`). This name is what the MCP server reports in its handshake; rename the directory to rename the server identity.

## Path Resolution

Relative `log_destination` and `binary_path` values resolve relative to the config file's directory, not the shell's current working directory. Paths beginning with `~/` expand against the user's home directory. Mercurius creates the `log_destination` leaf directory when the parent exists and is writable.
