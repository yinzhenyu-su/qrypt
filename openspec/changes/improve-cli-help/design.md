## Context

See `proposal.md` — Why. Current state that shapes the approach:

- The CLI is a cobra tree built by `NewRootCommand` (`internal/cli/command_root.go`). The root sets
  `SilenceUsage: true`, has a `Long` describing config discovery, and prints help when run with no
  subcommand.
- Usage errors already carry ad-hoc next steps: `installFlagErrorHelp` appends
  `Run 'X --help' for valid flags.`, `commandGroupArgs` appends `Run 'X --help' to see available
  commands.`, and commands such as `offline` append specific hints like `Run 'qrypt offline list'`.
- Commands carry `Short`, and some carry `Long`/`Example` (for example `offline fetch`); there is no
  check that every command does.
- Progressive disclosure already has one precedent: `fs copy` hides `--overwrite` with
  `MarkHidden`.
- Completion hooks exist (`ValidArgsFunction: cliruntime.NoFileCompletions`) but completion is not a
  documented product surface.
- The user reference lives in `docs/for-user/cli.md`; a CI step already regenerates and diffs some
  user docs (`scripts/ci-check.sh`, "Generated docs in sync").

## Goals / Non-Goals

**Goals:**

- A documented, testable help ladder (error next-step → group overview → command help →
  machine-readable reference) that a user can walk from an error to the reference.
- One place that builds usage errors so the "runnable next step" rule cannot drift.
- Progressive flag disclosure without hiding capability.

**Non-Goals:**

- Rewriting every command's prose in one pass; the contract and the inventory are separate.
- Localization, an interactive TUI, or a pager.
- Changing what any command does at runtime; this is the help and discovery surface only.

## Decisions

### D1. A five-level ladder is the contract

L0 error next-steps, L1 group overview, L2 command help, L3 topics, L4 machine-readable help and
completion. Each level is an observable behavior with a scenario.

- Alternative: one `--help` plus a man page. Rejected — nothing guides a user who does not already
  know the command.
- Alternative: document it only in `docs/`. Rejected — not discoverable in the terminal.

### D2. One builder owns the usage-error next step

A single helper appends the concrete next-step command, and every usage error goes through it. The
next step must be a real command line, not a bare "see --help".

- Alternative: keep per-command ad-hoc hints. Rejected — they already vary in shape and can silently
  lose the next step.

### D3. Typo suggestions come from cobra

Enable `SuggestionsMinimumDistance` on the root and use `SuggestFor` where a name is genuinely
ambiguous.

- Alternative: a custom edit-distance pass. Rejected — cobra already renders the suggestion and the
  unknown-command path.

### D4. The self-describing rule is enforced by a test over the tree

A contract test walks every command and fails on any missing summary, long description, or example.

- Alternative: a review checklist. Rejected — not enforceable and regresses silently.

### D5. Flags are grouped, with advanced flags hidden behind deep help

Common flags render by default; advanced flags are `MarkHidden` and revealed by `--help --advanced`,
which uses a custom help function to unhide them for that invocation. Hidden flags keep working.

- Alternative: show every flag. Rejected — that is the wall of flags this change exists to remove.
- Alternative: a bespoke `--all` flag. Rejected — it collides with real flags such as `fs list --all`,
  so the deep-help form uses `--advanced`.

### D6. Machine-readable help is derived from the command tree

A structured form of a command's help is produced from the cobra tree (commands, args, flags) rather
than by parsing the rendered text.

- Alternative: parse the `--help` string. Rejected — brittle against wording changes.

### D7. Completion stays cobra's, but becomes a documented surface

Keep the built-in `completion` command, document it in the topic/help text, and add it to the
contract test.

- Alternative: hand-written completion scripts. Rejected — more to maintain for no gain.

## Risks / Trade-offs

- [Help-text churn across many commands] → enforce with the contract test; fix gaps as an inventory
  task rather than blocking the change on perfect prose.
- [Hiding a flag a user needs] → only genuinely advanced flags are hidden, hidden flags still work,
  and deep help explains `--help --all`.
- [Machine-readable schema churn] → version the structured output and keep it additive.
- [Group-with-no-args now prints help instead of failing] → this is the intended improvement; note it
  in the user docs.

## Migration Plan

1. Land L0 (one usage-error builder + typo suggestions) and the self-describing contract test first;
   fix the commands it flags.
2. Add flag grouping and `--help --all`; mark advanced flags per command.
3. Add machine-readable help and document completion.
4. Rollback: each layer is additive and independent; disabling a layer leaves the others intact.

## Open Questions

- Whether machine-readable help is spelled `--help --json` or a separate `qrypt schema` command; the
  spec only requires "a structured form", so either satisfies it.
- Which flags count as advanced for each command; a curation task, not a design choice.
