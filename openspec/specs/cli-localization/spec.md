# cli-localization Specification

## Purpose

Selects the language used for human-facing CLI text from an explicit override or the user's system locale, so help, parameter descriptions, generated configuration comments, and common output match the user's language while machine-readable output stays stable.

## Requirements

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

Command summaries, long descriptions, examples, flag descriptions, help section labels, built-in help and completion commands, and generated help/version flag text SHALL use the selected language when a catalog entry exists and SHALL fall back to English otherwise. Shell examples SHALL remain executable English command lines.

#### Scenario: Top-level help is localized

- **WHEN** a user runs `qrypt --help` with `--lang zh-CN`
- **THEN** the summary, long description, examples, and visible flag descriptions use their Simplified Chinese catalog entries

#### Scenario: Nested command help is localized

- **WHEN** a user runs `qrypt fs list --help` with `QRYPT_LANG=zh-CN`
- **THEN** that command's summary, description, examples, and flag descriptions use their Simplified Chinese catalog entries

#### Scenario: Help scaffolding is localized

- **WHEN** a user runs `qrypt fs list --help` with `--lang zh-CN`
- **THEN** the `Usage`, `Examples`, `Flags`, and `Global Flags` section labels are rendered in Simplified Chinese

#### Scenario: Built-in commands are localized

- **WHEN** a user runs `qrypt help` or `qrypt completion --help` with `--lang zh-CN`
- **THEN** the built-in command summaries, long descriptions, and shell subcommand help use their Simplified Chinese catalog entries

#### Scenario: Generated help flags are localized

- **WHEN** a user runs any command with `--help` under `--lang zh-CN`
- **THEN** the generated `-h, --help` flag description is rendered in Simplified Chinese
- **AND** the root `-v, --version` flag description is rendered in Simplified Chinese

#### Scenario: Examples remain executable

- **WHEN** a user views help under `--lang zh-CN`
- **THEN** command examples remain unchanged English command lines with their original flags and arguments

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

Human-facing `fs` and `offline` narrative, progress, summary, and detail-label output SHALL use the selected language when a catalog entry exists and SHALL fall back to English otherwise. Stable codes, table headers, inline `key=` attributes, diagnostic reports, JSON output, errors, and logs SHALL remain English.

#### Scenario: Status messages are localized

- **WHEN** a command prints a common status message while `--lang zh-CN` is active
- **THEN** the message is rendered in Simplified Chinese when a translation exists
- **AND** the English message is shown otherwise

#### Scenario: File operation output is localized

- **WHEN** a user runs `qrypt fs get`, `qrypt fs rm`, `qrypt fs copy`, or `qrypt fs sync` with `--lang zh-CN`
- **THEN** progress lines, summaries, and human labels are rendered in Simplified Chinese

#### Scenario: File metadata labels are localized

- **WHEN** a user runs `qrypt fs stat` without `--json` under `--lang zh-CN`
- **THEN** the field labels such as type, name, size, and modification time are rendered in Simplified Chinese
- **AND** stable values such as the file type code remain English

#### Scenario: Offline task output is localized

- **WHEN** a user runs `qrypt offline show`, `qrypt offline inspect`, or `qrypt offline list` without `--json` under `--lang zh-CN`
- **THEN** narrative text and human labels are rendered in Simplified Chinese
- **AND** task state, type, phase, and provider identifiers remain English

#### Scenario: Stable output remains English

- **WHEN** a user runs `qrypt fs pending --verbose`, `qrypt fs journal`, or any command with `--json` under `--lang zh-CN`
- **THEN** table headers, diagnostic report labels, field names, and JSON keys and values remain English

#### Scenario: Debug bundle status is localized

- **WHEN** a user runs `qrypt debug bundle` with `--lang zh-CN`
- **THEN** the completion message is rendered in Simplified Chinese

### Requirement: Machine-readable output remains stable

Structured JSON output, machine-readable schemas, and diagnostic reports SHALL keep their existing English field values and labels. Localized text MAY be exposed only as additive fields. Structured command metadata SHALL be derived from pre-localization English text so inherited flags do not leak translated usage.

#### Scenario: JSON driver schema stays stable

- **WHEN** a user runs `qrypt driver schema quark --json` with `--lang zh-CN`
- **THEN** existing JSON fields retain their English values
- **AND** any localized text is provided in additional fields

#### Scenario: Command schema JSON stays stable

- **WHEN** a user runs `qrypt schema fs` with `--lang zh-CN`
- **THEN** the machine-readable command description remains English

#### Scenario: Inherited flag usage stays English

- **WHEN** a user runs `qrypt schema fs list --lang zh-CN`
- **THEN** flag descriptions inherited from the root or parent commands, including `--lang`, `--config`, and `--mount`, remain English

#### Scenario: Diagnostic JSON stays English

- **WHEN** a user runs `qrypt debug collect`, `qrypt debug watch --json`, or a probe test or benchmark with `--lang zh-CN`
- **THEN** diagnostic field names and human-readable diagnostic messages remain English

### Requirement: Translation catalogs stay complete

English and Simplified Chinese CLI catalogs SHALL contain the same message keys. Every non-example Chinese value SHALL be translated rather than copied from English, translated format strings SHALL preserve the same verbs, and every user-visible built-in driver parameter SHALL have the required localized description.

#### Scenario: Catalog key mismatch fails CI

- **WHEN** a CLI message key exists in one catalog but not the other
- **THEN** CI rejects the change

#### Scenario: Untranslated Chinese entry fails CI

- **WHEN** a non-example Simplified Chinese catalog value is identical to its English counterpart
- **THEN** CI rejects the change

#### Scenario: Format verb mismatch fails CI

- **WHEN** a translated message uses a different set of format verbs than its English counterpart
- **THEN** CI rejects the change

#### Scenario: Built-in command coverage is enforced

- **WHEN** a built-in command or generated help flag lacks a catalog entry
- **THEN** the command-tree coverage check rejects the change

#### Scenario: Missing driver translation fails CI

- **WHEN** a built-in driver parameter lacks its required localized description
- **THEN** the schema check rejects the change
