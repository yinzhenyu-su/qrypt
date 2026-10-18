## Why

Users discover qrypt's command line unevenly. The root command explains itself and a few commands
carry examples, but there is no consistent ladder from "I mistyped a flag" to "show me the full
reference": typos get no correction, a command's advanced flags arrive as one wall, and the CLI text
and `docs/for-user/cli.md` can drift apart. Progressive help makes the surface self-teaching without
dumping usage on runtime errors.

## What Changes

- Define a layered help contract (L0–L4) and pin it with contract tests so it cannot regress.
- **L0 — errors teach**: every usage error ends with a runnable next-step command, and a mistyped
  command suggests the intended one.
- **L1 — groups orient**: a command group with no arguments prints help, and each group's long text
  carries a few task-oriented examples rather than a bare command list.
- **L2 — commands disclose**: every leaf command has a one-line summary, a "when to use" long text,
  and runnable examples; flags are grouped into common and advanced, with advanced flags hidden from
  the default help and revealed by `--help --all`.
- **L4 — help is machine-readable and completable**: a machine-readable form of a command's help for
  scripts and GUIs, plus explicit, documented shell completion.

Topic help (`qrypt help TOPIC`, level L3) is deferred to a follow-up change so this one stays focused
on the error, group, command, and machine-readable levels.

## Capabilities

### New Capabilities

- `cli-help`: progressive, layered command-line help and discovery — error next-steps and typo
  suggestions, group and per-command help, machine-readable help, and shell completion.

### Modified Capabilities

<!-- none: the CLI help surface is not specified by an existing capability -->

## Impact

- `internal/cli`: root and group command long text, the shared usage-error helpers, per-command
  examples, flag grouping and hidden-flag handling, a `help TOPIC` command, a machine-readable help
  output, and completion wiring.
- `docs/for-user/cli.md`: aligned with the embedded topic source so both stay in sync.
- Tests: `internal/cli/command_ux_test.go`-style contract tests that assert the ladder exists.
