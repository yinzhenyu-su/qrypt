# Tasks

## 1. Schema Relocation And Metadata

- [x] 1.1 Move `qrypt.schema.json` to `schema/qrypt.schema.json`, update `scripts/gen-driver-docs.py`, `scripts/gen-config-docs.py`, and repository references, then verify both generators run from the new path and produce the same docs as before.
- [x] 1.2 Add `schema/README.md` documenting schema ownership, `x-driver`, `x-i18n`, `x-param-type`, generation, and validation rules; verify the document names every required convention used by the change.
- [x] 1.3 Add `scripts/check-schema.py` and verify it passes on the current schema and fails on a temporary fixture with a missing `zh-CN` description.
- [x] 1.4 Add `x-driver.type`, `x-driver.order`, `x-i18n.zh-CN` titles/descriptions/intros, and `x-param-type` overrides to the schema; verify `python3 scripts/check-schema.py` passes and every built-in driver parameter has an English and `zh-CN` description.
- [x] 1.5 Add a schema test or checker assertion that every `x-driver.type` appears in `mountConfig.type.enum` and has a matching `allOf` block; verify the check fails when a driver enum entry is removed.

## 2. Schema-Driven Documentation

- [x] 2.1 Update `scripts/gen-driver-docs.py` to read driver names, order, intros, and descriptions only from the schema; delete `DRIVER_META`, `DRIVER_ORDER`, `DRIVER_INTRO`, `PARAM_DESCRIPTIONS`, and `DRIVER_PARAM_DESCRIPTIONS`; verify regenerated `docs/for-user/support-drivers.md` contains the expected Chinese titles, order, and conditional requirement text.
- [x] 2.2 Update `scripts/gen-config-docs.py` to read titles, descriptions, and i18n only from the schema; delete all `*_DOC_OVERRIDES` tables; verify regenerated `docs/for-user/full-config.md` has no English-only parameter descriptions and no change in section coverage.
- [x] 2.3 Add a generator regression test or CI assertion that a schema-only translation change appears in both generated docs without editing Python; verify by changing one temporary `zh-CN` description and regenerating.

## 3. Runtime ParamSpec And Code Generation

- [x] 3.1 Add `ParamSpec` and `ParamSpecFor` in `pkg/drive`, keep `ParamSchema` as a compatibility accessor, and verify existing callers compile with `go test ./pkg/drive ./pkg/config ./internal/cli/...`.
- [x] 3.2 Add `scripts/gen-drive-schema.py` that emits `pkg/drive/builtin_schema_gen.go` from `schema/qrypt.schema.json`; verify the generated file is deterministic, gofmt-clean, and contains every built-in driver with `Required` and `AnyOf` values.
- [x] 3.3 Update `ValidateMount` to enforce `Required` and `AnyOf`; add table tests for `189` (`cookie` or `username + password`), `sftp` (`password` or `private_key`), and `onedrive_app` (`client_id` or `client_key` plus common required fields); verify each satisfied and unsatisfied shape produces the expected result.
- [x] 3.4 Remove built-in `ParamDef` literals from driver `init()` functions and make those drivers call `drive.Register(name, factory)`; keep dynamic registration for `scopedfs` and test doubles; verify `go test ./pkg/drivers/... ./pkg/core ./pkg/mobile` and `qrypt driver schema <name>` for representative drivers.
- [x] 3.5 Add a test comparing `ParamSpecFor` for every built-in driver with the schema-derived definitions; verify deleting a parameter from the generated map makes the test fail.

## 4. CLI And Integration

- [x] 4.1 Update `internal/cli/config/template.go` and any other `#:schema` references to `./schema/qrypt.schema.json`; verify `qrypt config init` in a temp directory writes the new reference and `qrypt config validate` accepts the generated config.
- [x] 4.2 Verify `qrypt driver schema --json` remains backward-compatible for existing fields and exposes `requirements` only as an additive field; add or update CLI tests for required, secret, default, example, and alternative requirements.
- [x] 4.3 Update `scripts/ci-check.sh` and `.github/workflows/ci.yaml` to run schema completeness checks, regenerate Go metadata, and fail when either generated artifact is stale; verify locally with `scripts/ci-check.sh`.

## 5. Integration Verification

- [x] 5.1 Run `scripts/ci-check.sh` and verify all gates pass, including generated docs, generated Go metadata, schema checks, config tests, driver tests, and CLI tests.
- [x] 5.2 Run a manual end-to-end check with a localfs config: `config init`, `config validate`, `driver schema localfs`, `fs list /`, and a mount smoke if the environment supports FUSE; verify behavior matches the pre-change CLI except for the documented schema path change.

## Workflow follow-up

- Archive the change after review and CI pass.
- Verify the archived capability spec and final docs render correctly.
