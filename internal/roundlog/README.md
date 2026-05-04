# Round Log Format

M3 writes one markdown file per successful round at `<session_dir>/round-NN.md`.

Each log contains:

- YAML frontmatter with `session_id`, `round_number`, `opened_at`, `verdict`, `reviewers`, and `notes_recorded`.
- An artifact manifest table with `name`, `source_path`, `snapshot_path`, `size`, and `hash`.
- A `Reviewer outputs` section with one H3 subsection per reviewer.
- A mutable notes region bounded by `<!-- mercurius:notes-begin -->` and `<!-- mercurius:notes-end -->`.

Only the mutable notes region and the `notes_recorded` frontmatter field may be rewritten after initial creation. Artifact manifest rows and reviewer output blocks are immutable.
