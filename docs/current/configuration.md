# Configuration

Mercurius is configured by one YAML file per running server. By default the CLI loads `./mercurius.yaml`; pass another path with `--config`.

## Minimal Config

```yaml
name: my-project
log_destination: ./reviews
reviewers:
  - name: codex
    impl: codex
```

## Full Example

```yaml
name: my-project
log_destination: ./reviews
default_budget: 4
max_findings: 8
review_context: |
  Deployment: personal tool, single supervised implementer.
  Preference: simple over highly defensive when both achieve correctness.
prompt_overrides: |
  Focus on blockers in design clarity, work-order testability, and scope.
reviewers:
  - name: codex
    impl: codex
    binary_path: /usr/local/bin/codex
    model: gpt-5.5
    extra_args:
      - --some-flag
```

## Required Fields

- `name`: human-readable project name.
- `log_destination`: directory where session logs, status files, and snapshots are written.
- `reviewers`: non-empty reviewer list.
- `reviewers[].name`: unique reviewer name.
- `reviewers[].impl`: reviewer implementation. Current implementations are `codex` and `dummy`.

## Optional Fields

- `default_budget`: default maximum successful rounds per session. Default is `4`. `open_session.budget` can override it.
- `max_findings`: maximum blocking findings across `concerns` plus `questions` in a successful round. Default is `10`. Advisory notes do not count.
- `review_context`: free-form markdown inserted before review criteria. Use it to calibrate reviewer rigor.
- `prompt_overrides`: free-form markdown inserted in the project-specific guidance section of the prompt.
- `reviewers[].binary_path`: reviewer binary path. For `codex`, omitted means use normal executable lookup.
- `reviewers[].model`: reviewer model string passed to the reviewer implementation.
- `reviewers[].extra_args`: additional reviewer-specific CLI arguments.

## Path Resolution

Relative `log_destination` and `binary_path` values resolve relative to the config file's directory, not the shell's current working directory. Paths beginning with `~/` expand against the user's home directory. Mercurius creates the `log_destination` leaf directory when the parent exists and is writable.

## Multiple Reviewers

The config may list multiple reviewers, but the current session model selects exactly one reviewer. If multiple reviewers are configured, `open_session.reviewers` must name exactly one configured reviewer.

Panel mode is future work.
