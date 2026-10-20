# Design

## Context

See `proposal.md` for motivation and `specs/cli-localization/spec.md` for the behavioral contract.

The CLI builds a Cobra command tree in `internal/cli/command_root.go`. Command text is currently hardcoded in English, while driver parameter descriptions already carry `x-i18n.zh-CN` in `schema/qrypt.schema.json`. Generated driver metadata is produced by `scripts/gen-drive-schema.py`.

Two constraints shape the approach:

- `--lang` must be known before Cobra renders help, but Cobra parses flags only after the tree is built.
- CLI prose is not configuration schema data. Putting every command description and error message into `qrypt.schema.json` would mix a config contract with a UI message catalog.

## Goals / Non-Goals

**Goals:**

- Resolve a supported display language before building the command tree.
- Provide locale selection, catalog loading, command-tree localization, and fallback for all command help and flag usage.
- Localize driver parameter help, config-init comments, and common output where translations already exist.
- Keep driver parameter translations sourced from the configuration schema.
- Ship the complete catalog key structure now; translated values can be filled incrementally.
- Keep JSON and other machine-readable output stable.
- Make missing catalog entries and stale generated catalogs fail CI.

**Non-Goals:**

- Localizing runtime, validation, network, or driver error strings.
- Localizing log lines, debug JSON, task JSON, or protocol payloads.
- Adding languages beyond English and Simplified Chinese.
- Changing command names, flag names, exit codes, or JSON field names.

## Decisions

### Decision: Separate CLI message catalogs under `schema/i18n/`

Add:

```text
schema/
  qrypt.schema.json
  i18n/
    cli.en.json
    cli.zh-CN.json
```

The configuration schema keeps parameter metadata and its `x-i18n`. CLI command prose lives in the catalogs.

Rationale:

- The schema remains a config contract and editor input.
- CLI strings have a different lifecycle: command help changes with UX, parameter metadata changes with configuration.
- Keeping the catalogs under `schema/` preserves the "edit translations in one place" goal while separating concerns.

Alternatives considered:

- Add `x-cli-i18n` to `qrypt.schema.json`: one file, but makes the config schema large and mixes unrelated contracts.
- Put catalogs under `internal/cli/i18n/`: simpler to embed, but less discoverable and breaks the single-place editing goal.

Catalogs are keyed by stable message IDs:

```text
cmd.root.short
cmd.fs.list.long
flag.fs.list.json.usage
out.mount.mounted
```

A generator emits `internal/cli/i18n/catalog_gen.go` from the two JSON files. Codegen keeps the source catalogs in `schema/i18n/` without requiring `go:embed ../../schema`.

### Decision: Pre-scan arguments for `--lang` before building the command tree

`main` passes `os.Args[1:]` to the CLI entry point. The entry point scans for `--lang VALUE` and `--lang=VALUE` before `NewRootCommand`, resolves the locale, constructs a `Localizer`, builds the Cobra tree, and then applies localized text.

Rationale:

- Cobra renders help during `Execute`, and `--help` can short-circuit before a normal persistent pre-run hook.
- The flag must still exist in the tree so users can discover it and so completion/`qrypt schema` describe it.
- Pre-scanning is a small, testable helper.

Alternatives considered:

- Localize in `PersistentPreRun`: too late for help rendering.
- Build a custom help function that parses `--lang` lazily: possible, but spreads locale logic through help rendering.

`--lang` accepts `en`, `zh-CN`, and `auto` (default). `QRYPT_LANG` and system locale are consulted only when the flag is absent or `auto`.

### Decision: Use simple locale normalization instead of a new language dependency

Normalize locale strings by lowercasing, replacing `_` with `-`, stripping `.UTF-8`/`@modifier` suffixes, and matching prefixes:

- `zh`, `zh-cn`, `zh-hans` → `zh-CN`
- `en`, `en-us`, `en-gb` → `en`
- anything else → `en`

On Unix, check `LC_ALL`, `LC_MESSAGES`, then `LANG`; also consider the first supported entry in colon-separated `LANGUAGE`. On Windows, read the UI locale through `GetUserDefaultLocaleName` from `kernel32.dll` via `golang.org/x/sys/windows/syscall`, then normalize it.

Rationale:

- Only two languages are supported in this change; a full language-matching dependency is unnecessary.
- Prefix matching handles the common `zh_CN.UTF-8`, `zh-Hans`, and `en_US` spellings.

### Decision: Localize the command tree after construction

After building the Cobra tree, walk every command and replace `Short`, `Long`, `Example`, and flag usage from catalog keys derived from the command path and flag name. Commands that print status text call the localizer explicitly.

Rationale:

- Avoids changing every `NewCommand` signature and every command constructor.
- Keeps command names, `Use`, aliases, and flag names untouched.
- A test can walk the same tree and assert every generated key exists in both catalogs, preventing partial translations.

Runtime output keys are explicit at the call site, for example `loc.T("out.mount.mounted", path)`. Error strings are intentionally not routed through the catalog in this change.

### Decision: Driver parameter descriptions use generated localized metadata

Extend the generated `ParamDef` with a non-JSON localized map:

```go
type ParamDef struct {
    ...
    Description string
    DescriptionI18n map[string]string `json:"-"`
}
```

`ParamSchemaForLocale` or a small helper uses the selected locale with English fallback. Human-readable `qrypt driver schema` uses the localized description; JSON keeps the existing English `description` field.

Rationale:

- Parameter translations stay in `schema/qrypt.schema.json`, consistent with the documentation.
- Existing JSON consumers keep stable output.

### Decision: Config template accepts a localizer

`GenerateTemplate` takes the selected localizer (or localized descriptions) and renders section labels and driver parameter comments in the selected language.

Rationale:

- `config init` is human-facing and a major first-use surface.
- The template is not a machine-readable artifact; localizing comments does not affect config semantics.

### Decision: Tests force a known language

CLI tests set `QRYPT_LANG=en` unless they explicitly test localization. This prevents developer and CI system locale from changing test expectations. Localization tests run with `--lang zh-CN` and with Unix-style locale aliases.

## Risks / Trade-offs

- **Localized output changes existing CLI snapshots** → Update tests to force `QRYPT_LANG=en`; document `QRYPT_LANG=en` as the compatibility escape hatch.
- **Catalog keys drift from command text** → Add a command-tree coverage test and a catalog parity check in CI.
- **Partial translations** → Missing values fall back to English. Catalog key coverage is complete now; Chinese values for advanced command help can be filled incrementally without code changes.
- **Windows locale detection differs from Unix** → Keep detection in build-tagged files with an injected function and unit-test the normalizer on all platforms.
- **`--lang` pre-scan can misread values** → Support only the two documented forms and fall back safely; add tests for `--lang=zh-CN`, `--lang zh-CN`, and invalid values.
- **Generated catalogs become another build step** → Check in `catalog_gen.go` and fail CI when regeneration changes it, matching the existing schema-generated Go pattern.
- **Machine output accidentally localized** → Keep JSON strings in English and test `driver schema --json` and `qrypt schema` under `--lang zh-CN`.

## Migration Plan

1. Add locale resolution and normalization with tests.
2. Add the two CLI catalogs, codegen, parity checks, and command-tree coverage checks.
3. Add the `--lang` flag and pre-scan; localize the command tree.
4. Localize driver parameter help and `config init`.
5. Localize common human-readable output.
6. Force English in existing tests and add Chinese-locale integration tests.
7. Run `scripts/ci-check.sh` and manual checks on Linux and Windows.

Rollback: localize output only when a supported non-English locale is selected. Setting `QRYPT_LANG=en` restores the previous text without reverting code. The catalogs and codegen can be removed independently of command behavior.
