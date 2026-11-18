# CHANGELOG

## Unreleased

CHANGE: **Tool results and session files change shape.** A field with nothing to report is now left out where it used to be `null`, an empty list is `[]`, and timestamps in tool results carry fractional seconds. Field names are unchanged.

CHANGE: **Tool arguments are checked strictly.** `session_id` is required wherever a tool needs one, and leaving it out answers `user_error` where it used to answer `not_found`. `round_number` must be a whole number of at least 1; leave it out of `collect_round` to get the latest round. Unknown arguments are refused.

CHANGE: **An unknown key in `mercurius.yaml` or the user-level config is an error.** The error names the key and the file, so a mistyped `review_focus` or `do_not_flag` no longer loads silently and runs a round without it.

FIX: A finding with an empty `id` is now refused. Two findings with empty ids used to pass as a valid review, and decisions recorded against them could not be told apart.

FIX: A reviewer name containing YAML punctuation, such as a colon, no longer corrupts the frontmatter of a round log.

CHANGE: Updated `github.com/michaelquigley/df` from `v0.3.12` to `v1.0.6`.

## v0.2.0

CHANGE: **The reviewer moves out of `mercurius.yaml` into a user-level config, `~/.config/mercurius/config.yaml`.** It is set once per installation and every project uses it. Run `mercurius bootstrap --user` to write a starter, then remove the `reviewer` block from each project file; a project file that still has one is refused. A project that needs its own reviewer uses a `reviewer_override` block, and each round reports which of the two it ran on.

CHANGE: **Session directories written by earlier versions are not read, and `status.json` is gone.** Sessions now live entirely on disk, so a session survives a server restart and any server process can collect, note, or close it. `session_status` reports the reviewer, finding limit, state, and failure per round instead of per session.

FEATURE: A review round runs in its own detached process, so a harness restart or idle shutdown mid-round no longer costs the review; the next server collects it as usual. The synopsis marks each round that outlived its server.

FEATURE: Only one round runs at a time per project. A second `start_review_round`, from any session, answers `conflict` naming the round that is running, so a lost session id no longer turns into a duplicate review.

FEATURE: Rounds have a timeout, `reviewer.timeout`, defaulting to 20 minutes. A reviewer that hangs is killed and its round fails, unless it had already produced a valid review, which is kept.

CHANGE: The server starts without a `mercurius.yaml`. A missing or broken config comes back from the tool call as a `user_error` naming the file, instead of a server that exits at launch and shows only a closed connection.

CHANGE: The reviewer and `max_findings` are read fresh for every round, like the calibration, so an edit takes effect on the next round without restarting the server.

CHANGE: Failed rounds stay on disk under their own number, and the retry is the next round. `collect_round` returns the stored failure, and the reviewer's raw output is kept in the round directory for troubleshooting.

CHANGE: `mercurius monitor` shows every round with its state and reviewer, and reports a round whose runner has died as lost.

CHANGE: The pi reviewer is sealed off from the host's own pi configuration during a review. Extensions, skills, prompt templates, directory trust, and startup network operations are all disabled, so a project's `.pi/` setup cannot run inside a review.

## v0.1.1

FEATURE: Now includes release build infrastructure for linux/amd64.

## v0.1.0

Initial release.
