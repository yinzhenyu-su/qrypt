# Proposal

## Why

The CLI is currently English-only even though the project already carries Chinese parameter descriptions in the schema. Users on Chinese systems get English help, flag descriptions, generated config comments, and common command output, while the same information is already translated for the documentation.

## What Changes

- Detect the user's language from `--lang`, `QRYPT_LANG`, then the system locale (`LC_ALL`, `LC_MESSAGES`, `LANG` on Unix; the Windows UI language), falling back to English.
- Support `en` and `zh-CN` initially, with locale matching for variants such as `zh_CN.UTF-8` and `zh-Hans`.
- Keep driver parameter metadata in `schema/qrypt.schema.json` via `x-i18n`; add CLI message catalogs under `schema/i18n/cli.en.json` and `schema/i18n/cli.zh-CN.json`.
- Localize:
  - command summaries, long descriptions, examples, and flag usage;
  - driver parameter descriptions shown by `qrypt driver schema`;
  - comments in configs written by `qrypt config init`;
  - common human-readable command output and progress/status messages.
- Keep machine-readable JSON output stable and English by default; expose localized descriptions as additive fields where useful.
- Add CI checks that both catalogs contain the same keys and that every built-in driver parameter has the required `zh-CN` description.
- Ship the complete catalog structure and localization infrastructure now. Untranslated entries fall back to English; filling the remaining Chinese CLI copy is a follow-up.
- **BREAKING**: human-readable CLI output follows the system language when a translation exists. Scripts that depend on English text should set `QRYPT_LANG=en` or pass `--lang en`.

## Non-Goals

- Localizing runtime, network, or validation error messages in this change. Those remain English until a follow-up introduces stable error identifiers and localized error catalogs.
- Localizing log lines, debug JSON, or protocol/API payloads.
- Adding languages other than English and Simplified Chinese.
- Completing Chinese translations for every command description and example in this change. Missing entries fall back to English; translation coverage is tracked separately.

## Capabilities

### New Capabilities

- `cli-localization`: the CLI selects a supported language from an explicit override or the user's system locale, and renders human-facing help, parameter descriptions, configuration comments, and common output in that language while keeping machine-readable output stable.

### Modified Capabilities

- None. The existing layered-help behavior is unchanged; only the language of its text is selected at runtime.

## Impact

- `internal/cli` command construction and output helpers
- New CLI message catalogs under `schema/i18n/`
- `schema/qrypt.schema.json` parameter `x-i18n` consumption
- Generated driver metadata and `qrypt driver schema`
- `qrypt config init` template rendering
- CLI tests for locale selection, fallback, and catalog completeness
- Linux, macOS, and Windows locale detection
