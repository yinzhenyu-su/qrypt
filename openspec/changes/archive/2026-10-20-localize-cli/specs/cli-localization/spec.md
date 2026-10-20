# Spec Delta

## Purpose

Selects the language used for human-facing CLI text from an explicit override or the user's system locale, so help, parameter descriptions, generated configuration comments, and common output match the user's language while machine-readable output stays stable.

## ADDED Requirements

### Requirement: Language selection is explicit and predictable

The CLI SHALL choose its display language from `--lang`, then `QRYPT_LANG`, then the system locale, and SHALL fall back to English when no supported language matches.

#### Scenario: Explicit flag wins

- **WHEN** a user passes `--lang zh-CN`
- **THEN** human-facing CLI text is rendered in Simplified Chinese regardless of environment or system locale

#### Scenario: Environment override wins over system locale

- **WHEN** `QRYPT_LANG=zh-CN` is set and the system locale is English
- **THEN** human-facing CLI text is rendered in Simplified Chinese

#### Scenario: Unsupported locale falls back

- **WHEN** the selected locale is not English or Simplified Chinese
- **THEN** human-facing CLI text falls back to English

### Requirement: Locale aliases are normalized

The CLI SHALL match common locale spellings such as `zh_CN.UTF-8`, `zh-CN`, and `zh-Hans` to Simplified Chinese, and common English spellings to English.

#### Scenario: Unix Chinese locale is recognized

- **WHEN** `LANG=zh_CN.UTF-8` and no explicit override is set
- **THEN** the CLI selects Simplified Chinese

#### Scenario: English locale is recognized

- **WHEN** `LANG=en_US.UTF-8` and no explicit override is set
- **THEN** the CLI selects English

### Requirement: Help text uses available translations

Command summaries, long descriptions, examples, and flag descriptions SHALL use the selected language when a catalog entry exists and SHALL fall back to English otherwise.

#### Scenario: Top-level help is localized

- **WHEN** a user runs `qrypt --help` with `--lang zh-CN`
- **THEN** the summary, long description, examples, and visible flag descriptions use their Simplified Chinese catalog entries

#### Scenario: Nested command help is localized

- **WHEN** a user runs `qrypt fs list --help` with `QRYPT_LANG=zh-CN`
- **THEN** that command's summary, description, examples, and flag descriptions use their Simplified Chinese catalog entries

#### Scenario: Untranslated help falls back

- **WHEN** a selected language has no catalog entry for a command or flag
- **THEN** the English text is shown

### Requirement: Driver parameter help follows the schema translations

Human-readable driver parameter descriptions SHALL use the localized values defined by the configuration schema.

#### Scenario: Driver schema help is localized

- **WHEN** a user runs `qrypt driver schema quark` with `--lang zh-CN`
- **THEN** parameter descriptions are shown in Simplified Chinese

#### Scenario: Missing translation falls back to English

- **WHEN** a localized parameter description is missing
- **THEN** the English schema description is shown

### Requirement: Generated config comments follow the selected language

Comments and parameter descriptions written by `qrypt config init` SHALL use the selected language.

#### Scenario: Chinese config template

- **WHEN** a user runs `qrypt config init` with `--lang zh-CN`
- **THEN** generated comments and parameter descriptions are in Simplified Chinese

### Requirement: Common human-facing output uses available translations

Common status and progress messages printed by human-facing commands SHALL use the selected language when a catalog entry exists and SHALL fall back to English otherwise.

#### Scenario: Status messages are localized

- **WHEN** a command prints a common status message while `--lang zh-CN` is active
- **THEN** the message is rendered in Simplified Chinese when a translation exists
- **AND** the English message is shown otherwise

### Requirement: Machine-readable output remains stable

Structured JSON output and machine-readable schemas SHALL keep their existing English field values; localized text MAY be exposed only as additive fields.

#### Scenario: JSON driver schema stays stable

- **WHEN** a user runs `qrypt driver schema quark --json` with `--lang zh-CN`
- **THEN** existing JSON fields retain their English values
- **AND** any localized text is provided in additional fields

#### Scenario: Command schema JSON stays stable

- **WHEN** a user runs `qrypt schema fs` with `--lang zh-CN`
- **THEN** the machine-readable command description remains English

### Requirement: Translation catalogs stay complete

English and Simplified Chinese CLI catalogs SHALL contain the same message keys, and every user-visible built-in driver parameter SHALL have the required localized description.

#### Scenario: Catalog key mismatch fails CI

- **WHEN** a CLI message key exists in one catalog but not the other
- **THEN** CI rejects the change

#### Scenario: Missing driver translation fails CI

- **WHEN** a built-in driver parameter lacks its required localized description
- **THEN** the schema check rejects the change
