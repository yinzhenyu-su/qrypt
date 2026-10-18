## Purpose

Let users learn the command line progressively: each step — an error, a group listing, a command's
help, or a machine-readable reference — shows only the next useful layer, so the surface teaches
itself instead of dumping a wall of flags.

## ADDED Requirements

### Requirement: Help is layered and progressive

The CLI MUST expose a help ladder a user can walk without leaving the tool: an error next-step, a
command-group overview, per-command help, and a machine-readable reference. Each level MUST lead to
the next with a runnable command, and reaching a deeper level MUST NOT require the user to already
know the command they want.

#### Scenario: A command group with no arguments prints its help

- **WHEN** a user runs a command group such as `qrypt fs` or `qrypt offline` with no subcommand
- **THEN** the group prints its help with its subcommands and examples instead of failing

#### Scenario: A command's help is reachable from its group

- **WHEN** a user reads a command group's help
- **THEN** it names how to open each subcommand's help, and that command's help opens the next level

#### Scenario: The machine-readable reference is reachable from the top level

- **WHEN** a user reads the top-level help
- **THEN** it names the machine-readable form and how to open it

### Requirement: Usage errors end with a runnable next step

Every usage error MUST end with at least one copy-pastable command that advances the user, and MUST
NOT print the full usage block. A runtime (non-usage) failure MUST print its error without the usage
block. The next-step command MUST be concrete (a real command line), not a bare "see --help".

#### Scenario: Unknown flag suggests valid flags

- **WHEN** a user passes a flag a command does not define
- **THEN** the error names the flag and ends with a command that shows the valid flags for that command

#### Scenario: Missing argument suggests how to proceed

- **WHEN** a user omits a required argument
- **THEN** the error names the missing argument and ends with a runnable command that supplies it or lists the candidates

#### Scenario: Runtime failure does not dump usage

- **WHEN** a command fails for a runtime reason such as a missing config or an unreachable Core
- **THEN** the command prints the error without the usage block

### Requirement: Mistyped commands suggest the intended one

A command name close to a real subcommand MUST be answered with a suggestion naming the intended
command, rather than only an unknown-command error.

#### Scenario: A close typo is corrected

- **WHEN** a user runs a subcommand name that is a near-miss of a real one, such as `qrypt fs copie`
- **THEN** the error suggests the intended command `copy`

### Requirement: Every leaf command is self-describing

Every runnable command MUST carry a one-line summary, a long description explaining when to use it,
and at least one runnable example. A command that lacks any of these MUST fail the help contract
check.

#### Scenario: Help shows summary, description, and examples

- **WHEN** a user opens a leaf command's help
- **THEN** it shows a one-line summary, a description of when to use the command, and at least one copy-pastable example

#### Scenario: The contract check enumerates every command

- **WHEN** the help contract check runs over the command tree
- **THEN** it reports any command missing a summary, description, or example

### Requirement: Flags are disclosed progressively

Each command MUST distinguish common flags from advanced flags. Advanced flags MUST be hidden from the
default help and revealed only by an explicit deep-help form such as `--help --advanced`. A hidden flag
MUST still work when passed.

#### Scenario: Default help hides advanced flags

- **WHEN** a user opens a command's default help
- **THEN** only the common flags are listed

#### Scenario: Deep help reveals advanced flags

- **WHEN** a user opens a command's help with the deep-help form
- **THEN** the advanced flags are listed alongside the common ones

#### Scenario: A hidden flag still functions

- **WHEN** a user passes an advanced flag that the default help hides
- **THEN** the command honors it as usual

### Requirement: Help is machine-readable and shell-completable

A command's help MUST be obtainable in a structured, machine-readable form for scripts and clients.
The CLI MUST support generating shell completion for the common shells, and MUST document how.

#### Scenario: Structured help is emitted

- **WHEN** a user requests a command's help in the machine-readable form
- **THEN** the output is structured data describing the command's subcommands, arguments, and flags

#### Scenario: Completion scripts can be generated

- **WHEN** a user requests completion for a supported shell
- **THEN** the CLI emits a completion script for that shell
