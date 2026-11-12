---
title: bind project data through dd
state: inbox
created: 2026-11-11
tags: [enhancement]
log:
  - stamp: 2026-11-11
    note: surfaced while bootstrapping the terminus rubric — docs/journal/2026-11-11.md
---

move the project structs that still carry json tags onto `github.com/michaelquigley/df/dd`. three surfaces: the on-disk session files (`internal/store` and the runner lock's note), the MCP tool payloads and error envelope (`internal/mcpserver`), and the review-output parse (`internal/schema`). keep every file key and tool field name exactly as it is. leave the claude envelope and pi event-stream types alone; those are other tools' formats.

terminus already does all of this and is the worked example: `internal/mcpserver/binding.go` registers tools through the SDK's raw handler and binds with `dd`, and `internal/report/codec.go` carries the converter that passes a `json.RawMessage` through `dd` as nested json. the six tool input schemas have to be written by hand once the SDK's typed `AddTool` is gone; `record_round_notes` already has one.

it can land one surface at a time. when all three are done, ask michael to promote `golang-conventions/df-binding` to blocking in the mercurius rubric in `terminus-canon`.

## why

`dd` everywhere it is possible is the house rule. the json-tagged payloads date from the SDK's typed `AddTool` in the M4 commit and no reason for them was ever recorded. the rubric adopts `df-binding` as advisory in the meantime, so new code is nudged toward `dd` and the existing surfaces show up as known findings rather than surprises. not urgent.
