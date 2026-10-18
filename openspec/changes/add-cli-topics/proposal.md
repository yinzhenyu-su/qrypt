## Why

Some qrypt concepts span many commands — config discovery, exit codes, encryption, the offline
task model — so a user cannot learn them from any single command's `--help`. Today that knowledge
lives only in `docs/for-user/cli.md`, which the terminal does not reach and which can drift from the
CLI. Curated topic help closes that gap without leaving the tool.

## What Changes

- Add `qrypt help TOPIC` (for example `config`, `exit-codes`, `offline`) that renders curated topic
  text, and list the available topics when run with no topic.
- An unknown topic fails with the list of available topics.
- Source topic text from a single embedded document set that the shipped documentation also uses, and
  check in CI that the two cannot drift.

## Capabilities

### New Capabilities

- `cli-topics`: curated, in-terminal topic help whose text has a single source shared with the shipped
  documentation.

### Modified Capabilities

<!-- none -->

## Impact

- `internal/cli`: the `help` command (or a `topics` command) and an embedded topic source.
- `docs/for-user/cli.md`: aligned with the embedded topic source.
- `scripts/ci-check.sh`: a drift check that fails when the topic source and the docs disagree.
- Tests for topic rendering, the unknown-topic path, and the drift check.
