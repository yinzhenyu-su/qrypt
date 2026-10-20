# Tasks

## 1. Locale Resolution

- [x] 1.1 Add an `internal/cli/i18n` package with supported languages, locale normalization, and `Localizer` lookup/fallback; verify unit tests cover `zh_CN.UTF-8`, `zh-Hans`, `en_US`, and unsupported locales falling back to English.
- [x] 1.2 Add Unix locale detection from `LC_ALL`, `LC_MESSAGES`, `LANG`, and `LANGUAGE`; verify tests cover precedence and colon-separated `LANGUAGE` values.
- [x] 1.3 Add Windows UI-locale detection behind a build tag using `GetUserDefaultLocaleName`; verify the Windows test compiles and normalizes Chinese and English locale names.
- [x] 1.4 Add the persistent `--lang` flag plus `QRYPT_LANG` handling and pre-scan `os.Args` before building the command tree; verify `--lang zh-CN`, `--lang=zh-CN`, invalid values, and environment precedence.

## 2. Message Catalogs

- [x] 2.1 Add `schema/i18n/cli.en.json` and `schema/i18n/cli.zh-CN.json` with keys for the root command, every command summary/long description/example, and every flag usage; verify `python3 scripts/check-cli-i18n.py` reports matching key sets.
- [x] 2.2 Add `scripts/gen-cli-i18n.py` to generate `internal/cli/i18n/catalog_gen.go` from the JSON catalogs; verify the generated file is deterministic, gofmt-clean, and current.
- [x] 2.3 Add a command-tree coverage test that walks every command and flag, derives its catalog keys, and fails when either catalog is missing a key; verify removing one key makes the test fail.
- [x] 2.4 Wire catalog parity, generated-catalog freshness, and command-tree coverage into `scripts/ci-check.sh` and `.github/workflows/ci.yaml`; verify the new checks run locally.

## 3. Command Help And Output

- [x] 3.1 Add command-tree localization for `Short`, `Long`, `Example`, and flag usage after the tree is built; verify `qrypt --help` and `qrypt fs list --help` contain Chinese text under `--lang zh-CN` and English text under `--lang en`.
- [x] 3.2 Add localizer calls to common human-facing status/progress output; verify the affected commands render Chinese messages under `QRYPT_LANG=zh-CN`.
- [x] 3.3 Keep machine-readable output stable by leaving `qrypt schema` and JSON command output English; verify `qrypt schema fs` and `qrypt driver schema quark --json` are unchanged under `--lang zh-CN`.

## 4. Driver And Config Localization

- [x] 4.1 Extend generated `ParamDef` metadata with localized descriptions from schema `x-i18n`, keeping JSON output English; verify `qrypt driver schema quark` uses Chinese descriptions under `--lang zh-CN` and falls back to English when a translation is absent.
- [x] 4.2 Thread the localizer into `qrypt config init` so section labels and parameter comments use the selected language; verify generated configs under `--lang zh-CN` contain Chinese comments and still pass `qrypt config validate`.

## 5. Verification And Compatibility

- [x] 5.1 Force `QRYPT_LANG=en` in existing CLI tests that assert English text; verify the full CLI test suite passes regardless of the developer's system locale.
- [x] 5.2 Add integration tests for `--lang zh-CN`, `QRYPT_LANG=zh-CN`, unsupported-locale fallback, translated help where a catalog entry exists, localized driver schema, and localized config init; verify all three entry points agree and untranslated strings fall back to English.
- [x] 5.3 Update the CLI and quickstart documentation with the language-selection precedence and the `QRYPT_LANG=en` compatibility escape hatch; verify the documented commands behave as written.
- [x] 5.4 Run `scripts/ci-check.sh` and manual checks for English, Chinese, and unsupported locales on Linux; verify JSON output, exit codes, and command names remain unchanged.

## Workflow follow-up

- Archive the change after review and CI pass.
- Verify the archived `cli-localization` capability and documentation render correctly.
