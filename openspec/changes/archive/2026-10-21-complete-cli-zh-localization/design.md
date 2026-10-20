# Design

## Context

See `proposal.md` for motivation and `specs/cli-localization/spec.md` for the behavioral contract.

The current localization layer replaces command `Short`/`Long`/`Example` and flag usage from `schema/i18n/cli.*.json` after the Cobra tree is built. Three constraints shape the completion work:

- Only 40 of 361 non-example Chinese values are translated; the rest are English placeholders that pass the current key-parity check.
- Cobra adds the default `help` and `completion` commands and the `-h`/`-v` flags during `Execute`, after `LocalizeCommandTree` has run, so they stay English. The usage template labels (`Usage`, `Flags`, ...) are also Cobra defaults and are never localized.
- Localization mutates flag usage in place. `schemaFor` then falls back to the mutated value when a per-command catalog key is absent, so inherited flags leak Chinese into `qrypt schema`.

Additional code constraints:

- `pkg/syncer` owns the current sync summary formatter, but the architecture gate forbids `pkg` importing `internal/`. Human formatting must move to `internal/cli/fs` or receive labels as data.
- `fs get` writes progress directly to `os.Stderr`, so it cannot currently share the command's localizer or captured test streams.
- `text/tabwriter` and fixed-width `fmt` formatting assume each rune has display width 1; Chinese table headers would misalign.

## Goals / Non-Goals

**Goals:**

- Render the complete Chinese help surface from one catalog, including Cobra scaffolding and built-in commands.
- Keep English help byte-for-byte stable unless a translation is explicitly selected.
- Localize `fs`/`offline` narrative and human labels without changing stable codes, aligned tables, JSON, diagnostics, errors, or logs.
- Make translation completeness and structured-output stability enforceable in CI.
- Keep the change reviewable by grouping tasks into catalog, help surface, structured output, and human output phases.

**Non-Goals:**

- Translating error strings, log lines, probe/debug JSON, or `fs journal` diagnostic reports.
- Translating table headers or aligned diagnostic tables.
- Localizing `version`, `uninstall`, `upgrade`, or `control` runtime output.
- Adding a Unicode-width table dependency for two table headers.
- Adding languages beyond English and Simplified Chinese.

## Decisions

### D1. Fill the existing catalogs rather than introduce a second translation source

Translate the 321 remaining non-example values directly in `schema/i18n/cli.zh-CN.json` and regenerate `catalog_gen.go`. Use `docs/for-user/cli.md` and `CONTEXT.md` as the terminology source.

Rationale:

- The catalog keys and fallback behavior already exist; the gap is data, not architecture.
- A second translation file or runtime service would duplicate the source of truth.

Alternatives considered:

- Derive translations from the Chinese documentation at runtime: not deterministic and not key-aligned.
- Add a translation service: unnecessary for two locales and an offline CLI.

### D2. Enforce translated values, not just key parity

Extend `check-cli-i18n.py` to reject a non-`.example` Chinese value that equals its English value, and to reject format-verb mismatches. Extend the command-tree coverage test to initialize built-in commands and generated flags before walking.

Rationale:

- The existing check reports 435 keys as complete while 321 are English placeholders.
- `.example` values are executable command snippets and must stay identical.
- Format-verb parity prevents `%!s(MISSING)` output in localized runtime messages.

Alternatives considered:

- Coverage thresholds or an allowlist of untranslated keys: hides the completion target and invites drift.
- Review-only enforcement: the previous change already showed this does not hold.

### D3. Build a Chinese usage template, but leave the English default untouched

When the selected language is not English, install a localized Cobra usage template on the root command. The template uses catalog keys for `Usage`, `Aliases`, `Examples`, `Available Commands`, `Additional Commands`, `Flags`, `Global Flags`, `Additional help topics`, and the trailing "more information" line. For English, keep Cobra's default template so existing output and tests remain byte-identical.

Rationale:

- Usage labels are part of the rendered help surface and cannot be translated by replacing command metadata alone.
- A language-conditional template avoids accidental English rewording.
- Templates inherit to subcommands, so one installation point covers the tree.

Alternatives considered:

- String-replace labels in rendered output: risks replacing words inside descriptions and is order-dependent.
- Always install a custom English template: unnecessary output churn.

### D4. Initialize Cobra built-ins before localization, and use generic help/version flag keys

Call `InitDefaultHelpCmd`, `InitDefaultCompletionCmd`, `InitDefaultHelpFlag`, and `InitDefaultVersionFlag` before `LocalizeCommandTree`. Add catalog entries for the `help` command, the `completion` command and its shell subcommands, and the generated `-h`/`-v` descriptions. Use single generic keys (`flag.help.usage`, `flag.version.usage`) for the generated flags rather than one key per command, and special-case those flags in the localizer and coverage check.

Rationale:

- Built-ins added during `Execute` are invisible to localization and coverage checks.
- The generated help flag text is uniform; 70+ duplicate per-command keys add noise without adding meaning.

Alternatives considered:

- Disable and reimplement Cobra's completion command: more code and more behavior to preserve.
- Per-command help flag keys: consistent with existing key naming but bloats the catalog.
- Leave generated flags English: leaves an obvious mixed-language line in every help page.

### D5. Preserve pre-localization English text in annotations for structured output

Before replacing command metadata or flag usage, store the original English text in command or flag annotations. `schemaFor` resolves text in this order: English catalog entry, stored English snapshot, current value. Apply the same rule to localized built-in commands.

Rationale:

- `qrypt schema` is a machine-readable contract and must stay English even when the command tree has been mutated.
- Inherited flags have no per-leaf catalog key, so the previous fallback was the mutated usage.
- Annotations are internal metadata and do not leak into JSON output.

Alternatives considered:

- Add a catalog key for every inherited flag on every leaf: combinatorial and still fragile.
- Build a second unlocalized command tree for schema output: expensive and risks divergence.
- Reconstruct English from flag ownership: Cobra does not expose the defining command directly.

### D6. Localize human output by category (L3b)

Adopt a fixed taxonomy:

| Output type | Examples | Treatment |
|---|---|---|
| Narrative/status/progress | downloaded, removed, copied, cancel requested, debug bundle written | Translate |
| Human summary/detail labels | bytes, duration, total/free/used, `fs stat` fields, `offline show`/`inspect` fields | Translate |
| Stable codes and values | state/type/phase/action/reason, provider IDs, file type codes | Keep English |
| Aligned tables and inline `key=` attributes | `fs pending`, `offline list`, `remote_name=` | Keep English |
| Diagnostic reports and JSON | `fs journal`, probe/debug reports, every `--json` | Keep English |
| Errors and logs | `fmt.Errorf`, domain failure reports, log lines | Keep English |

Rationale:

- Users get Chinese prose where it helps, while machine and diagnostic contracts stay stable.
- Table alignment remains correct without a new width-aware dependency.
- The `--json` and `QRYPT_LANG=en` escape hatches preserve script compatibility.

Alternatives considered:

- Translate everything human-visible, including tables and codes: misalignment plus broken ad-hoc parsing.
- Translate narrative only: leaves `fs stat` and `offline show` mostly English, undermining the goal.

### D7. Move sync summary formatting to the CLI layer

Create the localized sync summary in `internal/cli/fs` from `syncer.Result`. Keep `pkg/syncer.PrintSummary` as the English domain helper for non-CLI callers, but stop using it from the CLI.

Rationale:

- The architecture gate forbids `pkg` importing `internal/`.
- Passing a label struct into the domain package would put presentation concerns in `pkg/syncer`.

Alternatives considered:

- Add a `SummaryLabels` struct to `pkg/syncer`: workable, but keeps formatting and presentation in the domain.
- Leave sync summaries English: leaves a prominent `fs` command untranslated.

### D8. Thread the localizer and command streams through `fs get`

Pass the localizer and `cmd.ErrOrStderr()` into `get`, `getDir`, and `progressBar` instead of writing to `os.Stderr`. Add localizer parameters to `PrintEntryStat`, `PrintCopySummary`, and `PrintCopyDirSummary`.

Rationale:

- Progress output must use the selected language and must be capturable by tests.
- These helpers have few call sites and no external users in the repository.

Alternatives considered:

- A package-level global localizer: hidden state and unsafe for parallel tests.
- Keep `os.Stderr`: makes localized progress impossible to test and bypasses command output redirection.

## Risks / Trade-offs

- **Large catalog diff** → Review by command group and keep translation tasks in batches; the strict check makes progress measurable.
- **English help output accidentally changes** → Install the custom template only for non-English and keep English tests asserting default labels.
- **Built-in completion text drifts with Cobra upgrades** → Catalog entries intentionally override Cobra text; coverage tests catch missing keys, and a comment records the Cobra version.
- **English snapshots grow command annotations** → Negligible memory; annotations are not serialized.
- **Strict non-example check blocks future English-only keys** → Intended: new user-visible strings require a Chinese value, matching the driver-schema policy.
- **Human output tests depend on locale** → Existing tests run with English defaults; add explicit Chinese-output tests for representative commands.
- **Table headers remain English** → Accepted trade-off; documented as part of the stable-output boundary.
- **Mixed Chinese labels with English codes** → Accepted and intentional; codes remain stable for JSON and diagnostics.

## Migration Plan

1. Translate the remaining catalog entries in command-group batches and regenerate `catalog_gen.go`.
2. Add the strict catalog checks and extend coverage to built-ins and generated flags.
3. Localize the usage template, built-in help/completion commands, and generated help/version flags.
4. Add English snapshots and make `qrypt schema` ignore localized text.
5. Localize `fs`/`offline` human output and the `debug bundle` message using the L3b taxonomy.
6. Update `schema/README.md` and the `docs/for-user/cli.md` language note.
7. Run the CLI tests, schema checks, and `scripts/ci/check-all.sh`; manually verify English and Chinese help plus representative output.

Rollback: remove the localized usage template and built-in initialization, drop the output catalog keys and call sites, and restore the English-only summary path. English behavior and JSON contracts are unaffected throughout.

## Open Questions

None blocking. Whether to extend the same treatment to `version` and `uninstall` can be decided as a separate follow-up without changing this design.
