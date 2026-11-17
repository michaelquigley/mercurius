# Bind Project Data Through dd — Work Order

This is the plan for the roadmap card `bind-project-data-through-dd`: move the project structs that still carry `json` tags onto `github.com/michaelquigley/df/dd`. There is no separate spec. The card is the prompt, and the decisions that shape the work are recorded in the first section below.

Nothing here is implemented.

## Decisions

Made with Michael on 2026-11-17.

1. **Take dd's natural shape; the wire format may break.** The on-disk files and the tool results change shape where dd's defaults differ from today's. Sessions are run-and-done, so nothing needs to read an older shape. The alternative was to reproduce today's shape exactly with a tag on some thirty fields, which keeps a history of `encoding/json` habits alive in the structs for no reader's benefit.
2. **Fix the input-schema quirks.** The six tool input schemas are written by hand to say what the tools require. Five of them are inferred today by the MCP SDK from `json` tags, and the inference marks nothing as required.
3. **The config loader parses once and refuses unknown keys.** Today it parses the YAML a second time to look for four stale keys, and any other unrecognized key is silently ignored, so a mistyped `review_focus` runs a round with no focus and says nothing.

## What exists today

Read from the code on `main` at `v0.2.0`.

- **The library.** Mercurius is on `df` v0.3.12. Terminus, the worked example for this card, is on v1.0.3; the newest is v1.0.6. Strict binding, `DecodeStrictJSON`, the `+nullable` tag, and the guarantee of deterministic sorted-key output all arrived in v1.x.
- **On-disk files.** `internal/store/store.go` declares the shapes of `session.json`, `_run.json`, `_state.json`, `_review.json`, and `_notes.json` with 47 `json` tags, writes them with `json.MarshalIndent` in `WriteJSON`, and reads them with `json.Unmarshal` in `ReadJSON`. It defines its own `Duration` type with `MarshalJSON` and `UnmarshalJSON` so a timeout is stored as a string. `internal/runlock/runlock.go` does the same for the lock note, with four tags.
- **MCP payloads.** `internal/mcpserver/mcpServer.go` declares the tool inputs, outputs, and the error envelope with 101 `json` tags. Tools are registered with the SDK's typed `mcp.AddTool`, which infers each input schema from the tags and decodes arguments itself. `record_round_notes` is the one tool with a hand-written input schema. The handlers return `any` as their output type, so no output schema is advertised. Output timestamps are strings the server formats to whole seconds.
- **Review output.** `internal/schema/reviewOutput.go` validates the reviewer's raw output against the embedded JSON Schema, then `json.Unmarshal`s it into `ReviewOutput`, 19 tags. The schema already sets `additionalProperties: false` at every level and requires `suggestion`, which is nullable; reviewers routinely send `"suggestion": null`. `mcpserver` unmarshals the same raw output a second time to build the triage block.
- **Config.** `internal/config/config.go` binds both config files with `dd.MergeYAML`. `checkRenamedFields` then parses the project file again with `gopkg.in/yaml.v3` into a plain map to refuse `reviewer`, `reviewers`, `prompt_overrides`, and `default_budget`. The config structs carry no tags.
- **Tests.** `decodeStructured` in `internal/mcpserver/mcpServer_test.go` marshals a tool result and unmarshals it into the same Go struct that produced it, so a renamed key passes. The store tests round-trip through their own structs the same way. Only the `record_round_notes` input schema is pinned. The review-output tests feed the parser raw JSON literals, and the tool tests send arguments as raw maps, so those two directions are already pinned by their keys.

What stays on `encoding/json`, because it handles another tool's format or opaque bytes and binds no project struct: the claude envelope (`internal/reviewer/claude/envelope.go`), the pi event stream (`internal/reviewer/pi/jsonl.go`), JSON-object extraction (`internal/reviewer/jsonout`), the pretty-printing of raw reviewer output in `internal/roundlog` and `internal/prompt`, the edit of the schema document in `ReviewOutputSchemaWithMaxFindings`, and `json.RawMessage` as what a reviewer implementation returns. The copies of that output which mercurius stores and serves stop being bytes; see the target.

## What dd does

Checked with a scratch program against `df` v1.0.6, not taken from its documentation.

- A nil pointer is omitted. A nil slice unbinds as `[]` and a nil map as `{}`. `false`, `0`, and `""` are emitted.
- `dd:",+omitempty"` omits a zero value.
- Object keys are written in sorted order.
- `time.Time` unbinds as RFC 3339 with fractional seconds when present. `time.Duration` unbinds as its string form, `20m0s`, and binds back from it.
- A `map[string]any` field tagged `dd:",+opaque"` carries a JSON object through dd untouched. Read with a strict bind, its numbers stay `json.Number`, and unbinding writes them back digit for digit: `9007199254740993` and `1.50` both survive. Its keys come out sorted and a `null` inside it stays a `null`.
- Read with a forgiving bind, the same field's numbers pass through `float64`, and `9007199254740993` comes back as `9007199254740992`. Exactness depends on the strict read.
- `+opaque` refuses a `json.RawMessage` field, and an untagged `json.RawMessage` unbinds as a list of byte values. A struct cannot hold the raw bytes and be dd-bound.
- An explicit `null` bound into an untagged pointer field is an error, in forgiving and strict mode alike. `dd:",+nullable"` makes a null bind as absent.
- `dd.Strict()` rejects an unknown key, a missing `+required` field, a string where an integer is expected, and a non-integer number. `DecodeStrictJSON` also rejects duplicate keys, trailing data, and a document whose top level is not an object, and returns numbers as `json.Number`. A typed struct binds strictly from the tree it returns.
- A `map[string]any` field tagged `dd:",+extra"` receives every key of its own struct that `MergeYAML` did not match, with the key's whole value when that value is a map. It does not receive unmatched keys from inside a nested struct that did match; those are dropped unless the nested struct has a catch-all of its own. `Merge` has no strict mode.

## The target

### Tags

The structs carry a tag only where dd's default would be wrong:

- **`+omitempty`** where a zero value would state something false or read as a setting: `runner_pid` and `reviewer_pid` in `_state.json` (a round with no reviewer yet has no pid, not pid 0), the reviewer's `model` and `binary_path` wherever a reviewer is written, `log_path` on a round with no log, and `at` on an error with no time.
- **`+required`** on the tool inputs listed under the schemas below.
- **`+nullable`** on `Suggestion` in `schema.Concern` and `schema.AdvisoryNote`, which are only ever bound, never unbound. Nothing that is written out carries `+nullable`.
- **`+opaque`** on the raw reviewer object, which becomes a `map[string]any` wherever mercurius stores or serves it: in `_review.json` and in `collect_round`.
- **`+extra`** on a catch-all field in every struct dd binds from a config file. There are four: `projectFile`, `userFile`, `ReviewerConfig`, and `SettledDecision`. A struct left out is a place a mistyped key is still silently dropped, because unmatched keys inside a matched nested struct do not reach its parent's catch-all.

`AGENTS.md` rule 7 allows tags only for `+required`, `+extra`, and name overrides. It is reworded in the close-out to state the rule above.

### Wire changes

What an agent or a person reading the files sees change, and nothing else should:

| where | today | after |
| --- | --- | --- |
| every object | keys in struct order | keys sorted |
| `open_session.reviewer` with none configured | `null` | absent |
| `session_status.closed_at` on an open session | `null` | absent |
| `session_status.rounds[].completed_at`, `.error` | `null` when not applicable | absent |
| `collect_round.triage.next_finding` with no findings | `null` | absent |
| `collect_round.triage.findings`, `.advisory_notes` when empty | `null` | `[]` |
| a reviewer's `extra_args` when empty, in tool results and `_run.json` | absent | `[]` |
| `_state.json` `outlived_server` when false | absent | `false` |
| a stored error's `details` when empty | absent | `{}` |
| a decision's `note` when empty, in `_notes.json` | absent | `""` |
| timestamps in tool results | whole seconds | fractional seconds, as the files already have |
| the raw reviewer object in `_review.json` and `collect_round` | the reviewer's key order | keys sorted, same content |

The last row applies to the machine copies only. `_round.md` still prints the reviewer's output as it does today.

Tool-result timestamps change because the output structs take `time.Time` fields and let dd format them, which removes the server's own formatting helpers and makes a tool result agree with the file it was read from.

### Input schemas

Each is an explicit `jsonschema.Schema` with `additionalProperties: false` at every object level.

| tool | required | optional |
| --- | --- | --- |
| `open_session` | none | none |
| `start_review_round` | `session_id`; `artifacts`, at least one, each with `name` and `path` | none |
| `collect_round` | `session_id` | `round_number`, an integer of at least 1; omitted means the latest round |
| `record_round_notes` | `session_id`; `round_number`, at least 1 | `commentary`; `decisions`, each requiring `ref` and `disposition` from the existing enum, with optional `note` |
| `close_session` | `session_id` | none |
| `session_status` | `session_id` | none |

`session_id` is a plain string in the schema. Its format is still checked by the broker, so a malformed id keeps answering `not_found` as documented.

What a caller sees change:

- A missing `session_id` answers `user_error` from schema validation. Today it reaches the broker and answers `not_found`.
- An unknown key in `record_round_notes` is refused. Today that tool's schema leaves `additionalProperties` unset and accepts it.
- `round_number: 0` is refused by both tools that take it, and today the two differ. In `collect_round` it means the latest round today; omitting the field is the way to say that. In `record_round_notes` it answers `not_found` today, `round not found`, and afterward answers `user_error` from the schema.
- A missing `round_number` in `record_round_notes` answers `user_error`, because the field becomes required. Today an omitted value is zero and answers `not_found` like an explicit zero.
- An explicit `null` for any field is refused by the schema. `artifacts: null` already ends in a `user_error` today, from the broker's empty-list check; the code stays the same and the message changes.
- `round_number` written as `1.0` or `1e0` is refused. Today the SDK decodes and re-encodes the arguments, which turns those into `1`; dd's strict integer binding checks the number's spelling and accepts only an integer lexeme. This is accepted, not worked around: it is the same strictness as everything else at this boundary.
- An argument that fails the schema answers a tool error with code `user_error`, built by mercurius, with the validator's message as the cause. Stage 1 records what the SDK answers today so the change is visible.

### Strictness

- **Tool arguments:** `DecodeStrictJSON`, then the schema, then a strict `dd.Bind`. An arguments value that is absent or is `null` is treated as an empty object before any of that, for every tool. `open_session` accepts a `null` arguments value today, some clients send one for a tool with no parameters, and the strict decoder would otherwise refuse it for not being an object. A tool with required fields then fails on the missing field.
- **On-disk files:** a strict bind. A machine file with an unknown key or a wrong type answers `internal_error` naming the file, the same as an unparseable one does today.
- **Review output:** the JSON Schema validation runs first on the reviewer's bytes, unchanged, then a strict decode and a strict bind. The schema is already as strict as the bind, so the only new refusal is a duplicate key.
- **Config:** `MergeYAML` over the defaults, as today, with unknown keys collected by `+extra` and refused.

### Raw reviewer output

There is no converter and no shared set of dd options. The raw reviewer object is carried as a decoded value, not as bytes:

- A reviewer implementation still returns `json.RawMessage`. The runner validates those bytes against the schema as today, prints them into `_round.md` as today, and decodes them once with `DecodeStrictJSON`.
- From there the object is a `map[string]any` tagged `+opaque`: in `store.ReviewerResult`, in the broker's result type, and in the `collect_round` output. It is written to `_review.json` by an ordinary unbind and read back by an ordinary strict bind.
- The typed `ReviewOutput` is bound from that stored value directly. Nothing decodes the review a second time, and nothing re-marshals it to bytes to parse it.

Digit-exact numbers are dd's behavior under a strict read, shown above, not something mercurius implements. The stored and served copies have sorted keys; the reviewer's own bytes are what `_round.md` shows.

`df` could let `+opaque` accept a `json.RawMessage` field, which would let a struct keep the bytes. That is a card on `df`'s roadmap and nothing here waits on it.

## Stage 1 — pin the wire

Tests only, against today's code. Nothing in the tree outside test files changes.

- A helper that returns a tool result's structured content, and a file's content, as a generic JSON value with the volatile parts normalized: timestamps, pids, session ids, temporary paths, and hashes of temporary content. It decodes numbers as `json.Number`, so two values compare by their written digits. That holds for files, which the helper reads as bytes. It does not hold for tool results: the MCP client has already decoded those through `float64` before a test sees them, so the tool goldens say nothing about digits.
- Golden files under `testdata/`, rewritten by a `-update` flag, for:
  - each on-disk file: `session.json` open and closed; `_run.json`; `_state.json` running, completed, and failed; `_review.json`; `_notes.json`; the lock note.
  - each tool's result: `open_session` with a reviewer and with none; `start_review_round`; `collect_round` with findings and with none; `record_round_notes`; `close_session`; `session_status` over a running, a completed, and a failed round.
  - the error envelope for `user_error`, `not_found`, `conflict` with its details, and `reviewer_failed`.
  - the six input schemas as `ListTools` advertises them.
- A table of argument cases per tool, recording today's answer for each: an unknown key, a wrong type, a missing `session_id`, a missing `round_number`, an explicit `null`, `round_number: 0`, and `round_number: 1.0`. The last is sent as raw JSON bytes; passed as a Go map value it is normalized to `1` before it leaves the client and the case tests nothing.

The goldens compare decoded JSON, so key order does not register. That is deliberate: sorted keys are the one change every later stage makes everywhere, and a golden that broke on it would hide the changes that matter.

**Exit.** `make test` passes with the goldens generated from today's code. From here on, a stage regenerates only the goldens it means to change, and the golden diff in that stage has to be exactly the rows of the wire-changes table that belong to it.

Three rows of that table cannot show in a golden: the two about key order, because the comparison is on decoded JSON, and the one about timestamp precision, because timestamps are normalized. Sorted keys are dd's documented guarantee and are not re-tested here. Timestamp precision gets a direct assertion in stage 5.

## Stage 2 — upgrade df

`go.mod` moves `github.com/michaelquigley/df` to v1.0.6. No source change is expected: `dd.MergeYAML` and the `dl` calls mercurius uses exist unchanged in v1.0.6.

**Exit.** `make test` passes and no golden changes. If a golden or a config test changes, stop and report it; that is the upgrade changing behavior, and it is decided before anything is built on it.

## Stage 3 — the review-output parse

The schema package moves first, so that the store can hand it a decoded value in the next stage.

- `schema.ReviewOutput` and its nested types lose their `json` tags; the two `Suggestion` fields take `+nullable`.
- `ParseReviewOutput` validates the bytes as today, then decodes strictly and binds strictly. A second entry point binds a `ReviewOutput` from an already-decoded value, with no validation, for callers that hold a stored review.
- `mcpserver`'s triage block calls `ParseReviewOutput` instead of unmarshalling the raw bytes itself. It keeps skipping a result that does not parse, as it does today.

The store still holds the raw output as bytes in this stage, so every caller still has bytes to pass.

**Tests.** The existing literal-driven tests stand. Added: `suggestion` as `null`, as a string, and absent (absent is refused by the schema, as today); a duplicate key is refused; a `ReviewOutput` bound from a decoded value equals the one parsed from the same bytes.

## Stage 4 — the store and the lock note

- The store structs and the lock note lose their `json` tags and take the few `dd` tags the target lists. `store.Duration` is removed; the timeout is a `time.Duration`.
- `store.ReviewerResult.Raw` becomes a `map[string]any` tagged `+opaque`, and the broker's result type follows. The runner decodes the reviewer's bytes once, after validation and after writing `_round.md`. The raw field of the `collect_round` output takes the decoded value in this stage too, still under its `json` tag until stage 5; its content is the same, so its golden does not change.
- `WriteJSON` unbinds with `dd.UnbindJSON` and keeps the temporary-file-and-rename write. `ReadJSON` binds with `dd.Strict()`.
- The three places that parse a stored review bind the typed review from the stored value through the stage 3 entry point: the broker's synopsis assembly, the broker's ref check for notes, and `mcpserver`'s triage block. The runner's parse at round time still starts from the reviewer's bytes.

**Tests.** The file goldens regenerate, and their diff is the file rows of the wire-changes table. A machine file with an added unknown key answers `internal_error` naming the file. A raw object containing an integer above 2^53, a float written `1.50`, a `null`, and a nested object round-trips through `_review.json` with every one of them unchanged, compared as `json.Number`. This is a store-level test that writes the file itself: the review schema admits no numeric field, so no real round can carry one. The lifecycle test still deletes the markdown before collecting and closing, and its assertions are extended to check the review's content, which they do not today: after collecting, the collected raw object equals what the reviewer returned; in the synopsis, a concern's claim, a question's topic, and the reviewer's summary text are all present. Without those, a synopsis that dropped every finding when the markdown was gone would still pass.

**Docs.** `docs/current/architecture.md`, where it describes the files.

## Stage 5 — the MCP payloads

- A binding adapter in `internal/mcpserver`, ported from terminus's `internal/mcpserver/binding.go`: each tool is registered through the SDK's raw handler with its explicit input schema; absent or `null` arguments become an empty object; arguments are decoded strictly, validated, and bound; results and the error envelope are unbound with `dd.Unbind`.
- The six input schemas from the target.
- The input and output structs lose their `json` tags, take the tags the target lists, and the output timestamps become `time.Time`. The raw reviewer object in the `collect_round` output is the `+opaque` value. The server's time-formatting helpers are removed.
- `decodeStructured` is replaced by the stage 1 helper for shape, and by a `dd.Bind` from the structured content where a test wants a typed value.

**Tests.** The tool-result and schema goldens regenerate, and their diff is the remaining rows of the wire-changes table that a golden can show, plus the schemas in the target. The argument table changes in exactly the ways listed under the input schemas, and in no others. Because the helper normalizes timestamps, the fractional-seconds row of the wire-changes table never shows in a golden; one direct assertion covers it: a round's `started_at` in a tool result equals the value in its `_state.json`, written with a non-zero fraction. A reflection test walks the store, schema, and mcpserver payload types and fails on any `json` struct tag.

**Docs.** `docs/current/mcp-tools.md`: the result shapes, the required arguments, and what a schema failure answers.

## Stage 6 — the config loader

- The four structs dd binds from a config file each take a `+extra` field: the project file, the user-level file, the reviewer block, and a `settled_decisions` entry.
- The four stale keys are found in the project file's `+extra` map and keep their existing messages. Any other unknown key, at the top level of either file, inside a reviewer block, or inside a `settled_decisions` entry, is refused with a `user_error` naming the key and the file. An entry whose `do_not_flag` is deliberately blank is still accepted and still skipped, as today.
- `checkRenamedFields`'s second parse and the `gopkg.in/yaml.v3` import are removed, and `go mod tidy` is run.

**Tests.** A mistyped top-level key in the project file is refused and named. The same in the user-level file. A mistyped key inside `reviewer` and inside `reviewer_override` is refused and named. A `settled_decisions` entry with `do_not_flag` misspelled is refused, naming the key and the project file; without the entry's own catch-all it would load as a blank guard and be skipped. An entry with a blank `do_not_flag` loads. The four stale keys answer the messages they answer today. `_config.yaml` is still the project file's exact bytes.

**Docs.** `docs/current/configuration.md`: unknown keys are an error.

## Close-out

After stage 6 is accepted:

- `AGENTS.md`: the tech stack names dd for binding, not only config, and rule 7 is reworded to the tag rule in the target.
- `CHANGELOG.md`: one breaking entry for the changed tool results, files, and arguments, and one for unknown config keys.
- Flag for Michael, in the journal, the canon changes this makes due; do not edit the canon: promote `golang-conventions/df-binding` to blocking in the mercurius rubric; widen `projects/mercurius/dd-tag-minimalism` to admit `+omitempty` and `+nullable` and to cover every dd-bound struct, not only `internal/config`; drop the rubric characteristic that lists the json-tagged surfaces.
- Remove this work order. The roadmap card is Michael's to delete.

## Residuals

- **Strict reads are unforgiving across versions.** A machine file written by a newer build with a field this build does not know is an `internal_error`, not a tolerated extra. Sessions are short-lived and one build serves an arc, so this is accepted.
- **The stored reviewer output is not the reviewer's bytes.** The copy in `_review.json` and in `collect_round` is the decoded object written back with sorted keys. Its content is exact, numbers and nulls included; its byte layout is dd's. `_round.md` is where the reviewer's own bytes are shown.
- **Exactness rests on the strict read.** A forgiving bind of `_review.json` would round a large number without complaint. Every read of a machine file is strict, and the stage 4 round-trip test fails if one is not.
- **The typo refusal is all-or-nothing.** There is no way to mark a key as deliberately unknown. A config that carries a key for some other tool's benefit is refused.

## Critical files

| file | stage | change |
| --- | --- | --- |
| `internal/*/testdata/`, the wire helper | 1 | new: goldens and the normalizing helper |
| `go.mod`, `go.sum` | 2, 6 | df to v1.0.6; then `yaml.v3` dropped as a direct dependency |
| `internal/store/store.go` | 4 | dd tags; the raw reviewer object as an `+opaque` value; `Duration` removed; dd read and write |
| `internal/runlock/runlock.go` | 4 | the note read and written with dd |
| `internal/schema/reviewOutput.go` | 3 | tags; strict decode and bind after validation; a bind from a decoded value |
| `internal/runner/runner.go` | 4 | decodes the reviewer's bytes once; `time.Duration` timeout |
| `internal/broker/broker.go`, `types.go` | 4 | the raw reviewer object as a decoded value; stored reviews bound through the schema package; the `store.Duration` call site |
| `internal/mcpserver/binding.go` | 5 | new: the adapter |
| `internal/mcpserver/mcpServer.go` | 3, 4, 5 | triage through the schema package; then from the stored value; then schemas, tags, `time.Time` outputs |
| `internal/mcpserver/mcpServer_test.go` | 1, 5 | goldens; then `decodeStructured` replaced |
| `internal/config/config.go` | 6 | `+extra`; unknown keys refused; second parse removed |
| `docs/current/architecture.md`, `mcp-tools.md`, `configuration.md` | 4, 5, 6 | as listed per stage |
| `AGENTS.md`, `CHANGELOG.md` | close-out | as listed |
