# CHANGELOG

## Unreleased

CHANGE: The pi reviewer now hermetically seals review runs off from the host's development-flavoured pi configuration. Extension discovery (`--no-extensions` — extensions are how MCP servers arrive in pi), skills (`--no-skills`), and prompt templates (`--no-prompt-templates`) are disabled; the working directory is forced untrusted for the run (`--no-approve`), so a directory trusted in pi's trust store for interactive development can't execute its project `.pi/` extensions inside a review; and startup network operations are disabled (`--offline` — version check, managed binary and package installs, model catalog refresh; the model API call and oauth token refresh are unaffected). Flag set and `--mode json` event shape re-verified live against pi v0.80.7.

## v0.1.1

- FEATURE: Now includes release build infrastructure for linux/amd64.

## v0.1.0

Initial release.