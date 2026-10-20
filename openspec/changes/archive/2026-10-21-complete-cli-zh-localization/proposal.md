# Proposal

## Why

The CLI localization infrastructure landed, but only 40 of 361 non-example Chinese catalog entries are translated, and the help scaffold (`Usage`, `Flags`, `help`, `completion`) plus most human-readable `fs`/`offline` output still render in English. Chinese users therefore get a mixed-language CLI even when `--lang zh-CN` is active. Completing the deferred translation work also exposes an existing `qrypt schema` leak: localized inherited flag usage is reused as the English fallback for structured output.

## What Changes

- Translate all 321 remaining non-example CLI catalog entries (command summaries, long descriptions, and flag usage) in `schema/i18n/cli.zh-CN.json`; keep shell examples in English.
- Localize the help surface:
  - `Usage`, `Aliases`, `Examples`, `Available Commands`, `Flags`, `Global Flags`, and the trailing "more information" line;
  - Cobra's built-in `help` and `completion` commands;
  - the generated `-h, --help` and `-v, --version` flag usage.
- Localize human-facing `fs` and `offline` output, plus the `debug bundle` completion message:
  - narrative messages such as downloads, removals, copies, sync summaries, and task actions;
  - human summary and detail labels such as `bytes`, `duration`, `total/free/used`, `fs stat` fields, and `offline show`/`inspect` fields.
- Keep the following English by design:
  - `.example` command snippets, state/type/phase/action/reason codes, table headers, inline `key=` attributes, `fs journal` diagnostic reports, probe/debug JSON, all `--json` output, runtime errors, and logs.
- Fix structured-output stability: store pre-localization English command and flag text so `qrypt schema` stays English under `--lang zh-CN`, including inherited flags such as `--lang`, `--config`, `--mount`, and `--bwlimit-*`.
- Strengthen translation checks:
  - every non-`.example` Chinese value must differ from its English counterpart;
  - translated format strings must preserve the same `%s`/`%d` verbs;
  - command-tree coverage must initialize and include Cobra's built-in commands and help flags.
- Document the English-output escape hatch (`--lang en` / `QRYPT_LANG=en`) and the `--json` boundary for scripts.

## Capabilities

### New Capabilities

None.

### Modified Capabilities

- `cli-localization`: help scaffolding, built-in commands, and common `fs`/`offline` human output become localized; catalog completeness and machine-readable English stability gain enforceable requirements.

## Impact

- Translation catalogs and generated Go: `schema/i18n/cli.en.json`, `schema/i18n/cli.zh-CN.json`, `internal/cli/i18n/catalog_gen.go`.
- Help rendering and localization: `internal/cli/i18n`, `internal/cli/command_root.go`, `internal/cli/help.go`, `internal/cli/schema.go`.
- Human output: `internal/cli/fs`, `internal/cli/offline`, `internal/cli/journal`, `internal/cli/debug`, `internal/cli/runtime`.
- Sync summary formatting moves out of the domain package to respect the `pkg` → `internal` architecture boundary.
- Checks and tests: `scripts/schema/check-cli-i18n.py`, CLI localization/coverage/schema tests.
- Docs: `schema/README.md` and the language note in `docs/for-user/cli.md`.

Non-goals: localizing `version`, `uninstall`, `upgrade`, or `control` runtime output; translating probe/debug diagnostic JSON, error strings, or log lines; adding languages beyond English and Simplified Chinese.
