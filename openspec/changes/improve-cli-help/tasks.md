## 1. Foundation: error next-steps and self-describing commands

- [x] 1.1 Add one usage-error builder that always appends a runnable next-step command, and route the existing usage errors through it; verify a test asserts every usage error ends with a concrete `qrypt ...` command
- [x] 1.2 Enable cobra typo suggestions on the command tree; verify a near-miss subcommand error names the intended command
- [x] 1.3 Add a contract test that walks every command and fails on a missing summary, long description, or example; verify it reports offenders and passes once they are fixed
- [x] 1.4 Fill the summary/description/example gaps the contract test reports across the command tree; verify the contract test passes

## 2. Group overview (L1)

- [x] 2.1 Make every command group print its help when run with no subcommand; verify each group with no arguments prints help and exits successfully
- [x] 2.2 Add task-oriented examples to each group's long text; verify each group's help contains at least two runnable examples

## 3. Command help and progressive flags (L2)

- [x] 3.1 Group flags into common and advanced and hide advanced flags by default; verify a command's default help lists only its common flags
- [x] 3.2 Add a deep-help form (`--help --advanced`) that reveals advanced flags; verify it lists them and that a hidden flag still works when passed

## 4. Machine-readable help and completion (L4)

- [x] 4.1 Add a structured, machine-readable form of a command's help derived from the command tree; verify it describes subcommands, arguments, and flags as data
- [x] 4.2 Document and pin shell completion; verify the CLI emits a completion script for each supported shell and the help or a topic mentions it

## 5. Integration

- [x] 5.1 Add an end-to-end test that walks the help ladder from a usage error to machine-readable help; verify it passes
- [x] 5.2 Run the project CI check and the layered test scripts; verify they pass
