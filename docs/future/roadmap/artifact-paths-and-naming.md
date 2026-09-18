---
title: artifact paths and naming
state: researching
created: 2026-09-18
tags: [defect]
milestone: v0.1.x
---

two issues with artifacts:

- artifact paths must be _absolute_ (we probably should support project-relative paths)
- artifact names must not contain spaces.

agents seem to keep tripping over this. they can work around it, but they have to discover it themselves each time.
