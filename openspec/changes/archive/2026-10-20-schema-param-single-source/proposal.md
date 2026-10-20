# Proposal

## Why

Driver parameter metadata is currently duplicated across three layers: Go `ParamDef` literals, the hand-maintained `qrypt.schema.json` mirror, and Python documentation tables for names, ordering and Chinese descriptions. The copies already drift: conditional requirements such as `onedrive_app`'s `client_secret + tenant_id + email` were mis-rendered, and runtime-only types such as S3's `duration` are lost in the schema. Every new driver or parameter update has to touch several unrelated files.

## What Changes

- Move the canonical schema to `schema/qrypt.schema.json` and add a short `schema/README.md` documenting ownership, i18n and generation rules.
- Extend the schema with declarative metadata:
  - `x-driver` (`type`, `order`) on driver parameter definitions.
  - `x-i18n` for `zh-CN` titles, descriptions and per-driver intros.
  - `x-param-type` where the runtime type differs from the JSON Schema type (for example `duration`).
- Generate Go parameter metadata from the schema into `pkg/drive/builtin_schema_gen.go`.
  - Introduce `ParamSpec` (`Params`, `Required`, `AnyOf`) so conditional requirements are represented.
  - Built-in drivers stop declaring `ParamDef` literals; dynamically registered drivers such as `scopedfs` may keep passing their own definitions.
- Make `ValidateMount` enforce schema-derived required parameters and `anyOf` alternatives.
- Make the user-doc generators schema-only: remove `DRIVER_META`, `DRIVER_ORDER`, `DRIVER_INTRO`, `PARAM_DESCRIPTIONS`, `DRIVER_PARAM_DESCRIPTIONS` and the config `*_DOC_OVERRIDES` tables.
- Add CI gates for schema completeness, generated Go freshness and generated docs freshness.
- Update the generated config's `#:schema` reference and all repository references to the moved schema path.
- **BREAKING**: `qrypt.schema.json` moves to `schema/qrypt.schema.json`; existing configs that reference the old local path need updating.

## Capabilities

### New Capabilities

- `driver-parameter-schema`: the schema is the single source for driver parameter names, types, defaults, examples, secret markers, ordering, display metadata and translations; generated runtime metadata and user documentation both derive from it.

### Modified Capabilities

- None. The change is an internal metadata/validation refactor; user-facing command behavior stays compatible.

## Impact

- `qrypt.schema.json` → `schema/qrypt.schema.json`
- `pkg/drive` (`ParamDef`, new `ParamSpec`, generated metadata)
- `pkg/drivers/*` (remove built-in `ParamDef` literals)
- `pkg/config/validation.go` (required/`anyOf` validation)
- `scripts/gen-driver-docs.py`, `scripts/gen-config-docs.py`, new schema generator and checker
- `internal/cli/config/template.go`, `internal/cli/driver`
- `scripts/ci-check.sh`, `.github/workflows/ci.yaml`
- Documentation that references the schema path or documents driver parameters
