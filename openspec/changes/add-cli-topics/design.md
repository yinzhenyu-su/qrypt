## Context

See `proposal.md` — Why. Current state that shapes the approach:

- The CLI is a cobra tree (`internal/cli/command_root.go`). Cobra adds a default `help [command]`
  subcommand; a custom help surface must be installed explicitly (`SetHelpCommand`) rather than by
  adding a second command named `help`.
- `improve-cli-help` (a separate, in-flight change) establishes the help ladder this sits on top of:
  error next-steps, group help, per-command help, and `qrypt schema`. Topic help is the deferred level
  of that ladder.
- The user reference is `docs/for-user/cli.md`; `scripts/ci/check-all.sh` already regenerates and diffs
  some generated user docs.

## Goals / Non-Goals

**Goals:**

- `qrypt help TOPIC` renders curated concept text; no topic lists the topics; a command path still
  opens command help.
- One source of topic text, shared with the docs and checked in CI.

**Non-Goals:**

- Replacing `docs/for-user/cli.md`; topics are the in-terminal subset.
- Localization or a pager.
- A general documentation generator.

## Decisions

### D1. Install a custom `help` command instead of adding a second one

Override cobra's default help command with `SetHelpCommand` so `qrypt help COMMAND` keeps working and
`qrypt help TOPIC` is added. A command named `help` added alongside the default would collide.

- Alternative: a separate `qrypt topics TOPIC` command. Rejected — `help TOPIC` is the natural place,
  and the ladder already points users at `help`.

- Alternative: a hidden `--topic` flag on the default help command. Rejected — undiscoverable.

### D2. Resolve the argument as command-path first, then topic

`qrypt help X` resolves `X` against the command tree first, so command help keeps working; only when it
is not a command is it treated as a topic.

- Alternative: topics only. Rejected — it would break `qrypt help fs get`.

### D3. Topic text is embedded, and the docs are checked against it

Topic documents live in an embedded directory under `internal/cli` (`go:embed`), and `cli.md` includes
or is generated from the same text; a CI step fails when they diverge.

- Alternative: duplicate the text in Go constants and in the docs. Rejected — guaranteed drift.
- Alternative: read the docs from disk at runtime. Rejected — the binary must not depend on a docs
  checkout.

### D4. Keep the topic set small and curated

Start with the concepts that span commands (config discovery, exit codes, encryption, the offline task
model) rather than documenting every flag.

- Alternative: mirror the whole reference. Rejected — that is what `cli.md` and `qrypt schema` are for.

## Risks / Trade-offs

- [`help` overriding cobra's default changes `qrypt help` output] → keep `help COMMAND` behavior
  identical and covered by a test.
- [Embedded docs drift from `docs/`] → one source plus the CI diff step that fails on divergence.
- [Topic set grows unbounded] → curate to cross-command concepts; each topic is a reviewed file.

## Migration Plan

1. Land the embedded topic source and the `help TOPIC` dispatch; keep `help COMMAND` unchanged.
2. Align `cli.md` with the topic source and add the CI drift check.
3. Rollback: remove the topic dispatch and restore cobra's default help command; command help is
   unaffected.

## Open Questions

- Whether `cli.md` embeds the topic files verbatim or is generated from them; either satisfies the
  single-source requirement.
- The exact starting topic list; a curation decision, not a design one.
