# Spec Delta

## ADDED Requirements

### Requirement: Commands and flags have one owner and a real effect

Every user-visible command and flag MUST belong to exactly one command group and MUST have an effect;
flags without an implementation MUST NOT be registered. `core endpoint` information MUST be available
through `core status --json` (`endpoint`, `network`, `run_dir`), with `core endpoint` kept only as a
hidden compatibility alias. Crypt name helpers MUST live under `qrypt crypt`, not under `qrypt fs`.

#### Scenario: Dead fs flags are not advertised

- **WHEN** a user opens `qrypt fs --help`
- **THEN** neither `--mount` nor `--bwlimit`/`--bwlimit-download`/`--bwlimit-upload` appears

#### Scenario: status carries the endpoint without a Core

- **WHEN** a user runs `qrypt core status --json` while no Core is running
- **THEN** the output still includes the derived `endpoint`, `network` and `run_dir` fields

#### Scenario: journal is read-only from the CLI

- **WHEN** a user runs `qrypt fs journal --help`
- **THEN** only inspect-style commands are shown, and replay/prune are unavailable with a message pointing at Core-side reconciliation

#### Scenario: crypt helpers live outside fs

- **WHEN** a user opens `qrypt crypt --help`
- **THEN** `encode` and `decode` are available; `qrypt fs crypt-encode` still works as a hidden deprecated alias

### Requirement: Removed entry points have a deprecation path

Commands merged, moved or hidden by this change MUST remain runnable as hidden aliases for at least one
minor release, MUST print one deprecation notice naming the replacement, and MUST be absent from help,
the machine-readable reference and generated documentation during that window.

#### Scenario: Deprecated alias warns once

- **WHEN** a user invokes a deprecated alias such as `qrypt core endpoint`
- **THEN** the command runs normally and stderr contains one notice naming `qrypt core status`

#### Scenario: Help and machine-readable reference only document current entries

- **WHEN** the help tree, schema or shell completion is generated after this change
- **THEN** deprecated aliases are excluded and the current commands are fully documented
