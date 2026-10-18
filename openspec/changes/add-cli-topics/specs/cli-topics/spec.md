## Purpose

Give users curated, in-terminal explanations of qrypt concepts that span commands, sourced from the
same text as the shipped documentation so the CLI and the docs cannot drift apart.

## ADDED Requirements

### Requirement: Topic help explains cross-command concepts

`qrypt help TOPIC` MUST render curated topic text for a known topic, and running `qrypt help` with no
topic MUST list the available topics and how to open them. Command help for a real command path MUST
keep working.

#### Scenario: A known topic renders

- **WHEN** a user runs `qrypt help` for a known topic such as `config` or `exit-codes`
- **THEN** the curated topic text is printed

#### Scenario: No topic lists the options

- **WHEN** a user runs `qrypt help` with no topic
- **THEN** the available topics are listed with how to open each

#### Scenario: A command path still opens command help

- **WHEN** a user runs `qrypt help` for a real command path such as `offline fetch`
- **THEN** that command's help is printed instead of a topic

#### Scenario: An unknown topic lists the options

- **WHEN** a user runs `qrypt help` for an unknown topic
- **THEN** the command fails and lists the available topics

### Requirement: Topic text has a single source shared with the documentation

The topic text the CLI renders MUST come from one source that the shipped documentation also uses, and
a check MUST fail when the two disagree, so the terminal and the docs cannot drift.

#### Scenario: The drift check passes when in sync

- **WHEN** the topic source and the documentation agree
- **THEN** the check passes

#### Scenario: The drift check fails on divergence

- **WHEN** the topic source and the documentation disagree
- **THEN** the check fails and names the topic that drifted
