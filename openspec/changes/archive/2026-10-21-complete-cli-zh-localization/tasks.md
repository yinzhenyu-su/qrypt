# Tasks

## 1. Catalog Completion

- [x] 1.1 Translate the remaining `fs` command summaries, long descriptions, and flag usage in `schema/i18n/cli.zh-CN.json`; verify with a catalog filter that no `fs` non-example value still equals its English counterpart, and spot-check `qrypt fs --help --lang zh-CN`.
- [x] 1.2 Translate the remaining `probe test` command summaries, long descriptions, and flag usage; verify no `probe.test` non-example value equals English and spot-check `qrypt probe test --help --lang zh-CN`.
- [x] 1.3 Translate the remaining `probe bench` command summaries, long descriptions, and flag usage; verify no `probe.bench` non-example value equals English and spot-check `qrypt probe bench --help --lang zh-CN`.
- [x] 1.4 Translate the remaining `offline` command summaries, long descriptions, and flag usage; verify no `offline` non-example value equals English and spot-check `qrypt offline --help --lang zh-CN`.
- [x] 1.5 Translate the remaining `debug` command summaries, long descriptions, and flag usage; verify no `debug` non-example value equals English and spot-check `qrypt debug --help --lang zh-CN`.
- [x] 1.6 Translate the remaining `config`, `control`, and `driver` command and flag entries; verify no entries in those groups equal English and spot-check each group's `--help --lang zh-CN`.
- [x] 1.7 Translate the remaining root, `mount`, `upgrade`, `uninstall`, `version`, and `schema` entries; verify the catalogs have zero non-example values equal to English.
- [x] 1.8 Extend `scripts/schema/check-cli-i18n.py` to reject non-example Chinese values equal to English and to require matching format verbs; verify `python3 scripts/schema/check-cli-i18n.py` reports success and a deliberately reverted value fails.
- [x] 1.9 Regenerate `internal/cli/i18n/catalog_gen.go`, update `schema/README.md` to describe the completeness rule, and verify `python3 scripts/schema/gen-cli-i18n.py` leaves no further diff and `go test ./internal/cli/i18n` passes.

## 2. Help Surface

- [x] 2.1 Add catalog keys and a Chinese usage template for `Usage`, `Aliases`, `Examples`, `Available Commands`, `Additional Commands`, `Flags`, `Global Flags`, `Additional help topics`, and the trailing more-information line; verify `qrypt fs list --help --lang zh-CN` shows Chinese labels while `--lang en` keeps Cobra's default labels.
- [x] 2.2 Initialize Cobra's default `help` and `completion` commands before `LocalizeCommandTree` and add their summaries, long descriptions, and flag usage to both catalogs; verify `qrypt help help --lang zh-CN` and `qrypt completion --help --lang zh-CN` render Chinese.
- [x] 2.3 Localize the generated `-h, --help` and root `-v, --version` flag usage through shared catalog keys and update the coverage test to accept those generic keys; verify every help page shows a Chinese help-flag description under `--lang zh-CN`.
- [x] 2.4 Add tests for Chinese help scaffolding, built-in commands, generated help flags, and unchanged English help; update the language note in `docs/for-user/cli.md`; verify the new tests and the existing help tests pass.

## 3. Structured Output Stability

- [x] 3.1 Store original English command and flag text in annotations before localization replaces it; verify a test confirms the snapshots still hold English after a Chinese command tree is built.
- [x] 3.2 Make `schemaFor` resolve English text from the English catalog, then the stored snapshot, then the current value; verify `qrypt schema fs list --lang zh-CN` keeps inherited flag usage such as `--lang`, `--config`, and `--mount` English.
- [x] 3.3 Add a regression test comparing English and Chinese `schemaFor` output for summaries, descriptions, examples, flags, and advanced flags; verify it fails against the current localized fallback before the fix and passes after.

## 4. Human Output (L3b)

- [x] 4.1 Move the localized sync summary formatter into `internal/cli/fs` and stop calling `pkg/syncer.PrintSummary` from the CLI; verify Chinese summary output and unchanged English output in tests.
- [x] 4.2 Thread the localizer and `cmd.ErrOrStderr()` through `fs get`, `getDir`, and `progressBar`; verify Chinese download/progress messages and that tests can capture the output.
- [x] 4.3 Localize `fs` narrative and summary labels in `check`, `copy`, `df`, `du`, `rm`, `mv`, and `journal replay`/`prune`; verify representative Chinese output tests and unchanged JSON output.
- [x] 4.4 Add a localizer to `PrintEntryStat`, `PrintCopySummary`, and `PrintCopyDirSummary` and localize the `fs stat` and copy summary labels; verify both Chinese and English helper tests.
- [x] 4.5 Localize `offline` task detail labels, preview labels, empty-list text, background-transfer hint, and cancel/retry confirmations; verify task state, type, phase, and provider identifiers remain English.
- [x] 4.6 Localize the `debug bundle` completion message; verify the Chinese message and unchanged debug JSON output.
- [x] 4.7 Add regression tests that table headers, `fs journal` report labels, probe/debug JSON, `--json` output, and error strings stay English under `--lang zh-CN`; document the `--json` and `QRYPT_LANG=en` stability boundary in `docs/for-user/cli.md`.

## 5. Integration Verification

- [x] 5.1 Run the schema checks and generators (`check-schema.py`, `check-cli-i18n.py`, `gen-cli-i18n.py`, `gen-drive-schema.py`) and verify no stale generated files remain.
- [x] 5.2 Run `go test ./internal/cli/...` and `scripts/ci/check-all.sh`; verify both pass.
- [x] 5.3 Manually verify the Chinese matrix: `qrypt --help`, `qrypt fs list --help`, `qrypt probe bench --help`, `qrypt completion --help`, `qrypt fs stat`, `qrypt fs get`, and `qrypt offline show` under `--lang zh-CN`.
- [x] 5.4 Manually verify the English and machine-readable matrix: `--lang en` help is unchanged, `qrypt schema fs list --lang zh-CN` is English, and representative `--json` output is byte-compatible aside from allowed additions.

## Workflow follow-up

- Archive the change after review and CI pass.
- Verify the archived `cli-localization` capability and the updated language notes render correctly.
