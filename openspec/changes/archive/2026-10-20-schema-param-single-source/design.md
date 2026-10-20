# Design

## Context

See `proposal.md` for motivation and `specs/driver-parameter-schema/spec.md` for the behavioral contract.

Today the runtime and documentation read driver parameter metadata from different places:

- Built-in drivers pass `drive.ParamDef` values to `drive.Register`; `drive.ParamSchema` feeds config validation, `qrypt config init`, `qrypt config show`, and `qrypt driver schema`.
- `qrypt.schema.json` is a hand-maintained mirror used by editors and by `scripts/gen-driver-docs.py` / `scripts/gen-config-docs.py`.
- The doc generators additionally own driver names, ordering, intros, and Chinese descriptions.
- `pkg/config/config.go` owns global configuration structs, defaults, and validation behavior.

The schema already contains most declarative parameter data (`required`, `anyOf`, `default`, `x-secret`, `x-example`), but it is not complete enough to drive runtime metadata:

- It has no driver type/order metadata.
- It cannot express runtime-only types such as S3's `sign_url_expire` (`duration`).
- It does not carry localized descriptions.
- Conditional requirements (`required` plus `anyOf`) are not represented in `drive.ParamDef`.

## Goals / Non-Goals

**Goals:**

- Make `schema/qrypt.schema.json` the canonical source for built-in driver parameter metadata and its display translations.
- Generate runtime `ParamSpec` values from that schema so validation, CLI output, and config generation consume the same data.
- Make user documentation fully schema-driven, with no independent Python metadata tables.
- Add CI checks that fail on schema, generated-code, or generated-doc drift.
- Preserve dynamic driver registration for `scopedfs` and test doubles.

**Non-Goals:**

- Replacing Go configuration structs, TOML parsing, default normalization, or global validation with schema-driven runtime logic.
- Changing driver behavior, capability declarations, or filesystem semantics.
- Localizing CLI help output. CLI descriptions remain English; localized descriptions are used by documentation.
- Splitting the schema into multiple files before size or ownership requires it.

## Decisions

### Decision: Schema lives at `schema/qrypt.schema.json`

Move the canonical file from the repository root to a top-level `schema/` directory and add `schema/README.md` with editing, i18n, and generation rules.

Rationale:

- The schema is an input and a public contract, not a root-level config file.
- The directory gives future schemas and schema checks a natural home without putting them under `internal/` (implementation detail) or `docs/` (generated output).
- The root file can be removed after all repository references are updated.

Alternatives considered:

- Keep it at the root: lowest churn, but leaves a growing generated-input file among normal project files and does not scale to future schemas.
- Put it under `internal/schema/`: convenient for `go:embed`, but hides a file that editors and external tools need.

The config template's `#:schema` path must be updated. The design does not add a root compatibility copy; existing configs with `./qrypt.schema.json` are treated as a documented breaking change.

### Decision: Generate Go metadata instead of embedding the schema at runtime

A generator reads the schema and writes `pkg/drive/builtin_schema_gen.go`. The generated file contains a `map[string]ParamSpec` for built-in drivers.

Rationale:

- `pkg/drive` cannot `go:embed ../../schema/qrypt.schema.json`; Go embed patterns cannot traverse parent directories.
- Runtime JSON parsing would move schema-shape validation and error handling into the binary and slow startup for no benefit.
- Generated Go is typed, reviewable, and easy to compare in CI.

Alternatives considered:

- Runtime embedding via a copied schema file in `pkg/drive`: adds a second committed copy and a copy-step failure mode.
- Generate per-driver files in each `pkg/drivers/*` package: larger diff and unnecessary package coupling; one central map is enough.

The generated file is checked in and CI verifies it is current. It is not hand-edited.

### Decision: Introduce `ParamSpec` for requirements

Add:

```go
type ParamSpec struct {
    Params   []ParamDef
    Required []string
    AnyOf    [][]string
}
```

`ParamSchema(name)` remains as a compatibility accessor returning `ParamSpec.Params`; new code uses `ParamSpecFor(name)`.

Rationale:

- `ParamDef.Required` cannot express `cookie OR (username AND password)` or `password OR private_key`.
- Keeping `ParamSchema` avoids changing every existing caller in one step.
- `AnyOf` is a direct translation of JSON Schema `anyOf` and keeps the runtime contract aligned with editor validation.

Alternatives considered:

- Add `Alternatives` to each `ParamDef`: duplicates group data across every member parameter and is awkward to generate.
- Leave conditional requirements to driver factories: simpler, but violates the single-source goal and leaves validation weaker than the schema contract.

### Decision: Built-in drivers stop passing `ParamDef` literals; dynamic drivers keep the existing registration path

`drive.Register` keeps its variadic `schema ...ParamDef` parameter. Built-in drivers pass only the factory. `ParamSchema`/`ParamSpecFor` look up generated built-in metadata first, then fall back to parameters registered at runtime.

Rationale:

- `scopedfs` and test doubles register parameters dynamically and cannot be represented in the built-in schema.
- Keeping the variadic parameter avoids a broad API break and lets tests continue to construct fake drivers.

Alternatives considered:

- Remove `ParamDef` from `drive.Register` entirely: forces dynamic drivers to use a second registration API and increases churn without a functional benefit.

### Decision: Per-node `x-i18n` in the schema

Each user-visible property carries `x-i18n["zh-CN"].description`; each driver definition carries `x-i18n["zh-CN"]` with `title` and optional `intro`; each driver definition also carries `x-driver.type` and `x-driver.order`.

Rationale:

- Translations stay next to the parameter they describe, making omissions visible in review.
- JSON Schema validators ignore custom `x-` keywords, so editor validation is unaffected.
- `x-driver.type` resolves the mismatch between definition names (`baiduNetdiskParams`, `onedriveAppParams`) and runtime types (`baidu_netdisk`, `onedrive_app`).

Alternatives considered:

- A top-level translation map keyed by JSON Pointer: keeps nodes clean but makes paths fragile and translations hard to review.
- Sidecar locale files: avoids schema growth but duplicates structure and needs a merge step.

### Decision: Preserve runtime-only parameter types with `x-param-type`

The schema type is `string` for `sign_url_expire`, while runtime metadata reports `duration`. Add `x-param-type` and give it precedence during code generation.

Rationale:

- JSON Schema has no duration type, but the runtime type is useful in `qrypt driver schema` and config templates.
- Explicit override is preferable to silently changing CLI output or dropping the distinction.

### Decision: Global config stays Go-owned; schema owns its docs and i18n

`pkg/config` continues to parse TOML and validate behavior. The schema remains the source for global config documentation, editor hints, and Chinese descriptions.

Rationale:

- Global config has behavior that JSON Schema cannot express: custom duration/size parsing, `*bool` presence semantics, cross-field references, and runtime layout overrides.
- Generating config structs or replacing validation would turn this focused metadata change into a larger runtime rewrite with little user-visible benefit.

A reflection-based drift test may be added later, but is not required for this change.

## Risks / Trade-offs

- **Schema grows significantly** → Keep one file initially; split by `$ref` only if it becomes unmanageable. The schema checker enforces required metadata, so growth does not hide omissions.
- **Code generation becomes a required build step** → Check in the generated file and make CI fail when it is stale. Contributors do not need `go generate` for normal builds.
- **Moving the schema breaks existing editor references** → Update all repository references and call out the breaking path change in the release notes. Do not add a duplicate root file, which would reintroduce a second source.
- **Conditional requirements differ from current factory checks** → Add tests for `189`, `sftp`, and `onedrive_app` covering satisfied and unsatisfied alternatives; keep factory validation as the final guard.
- **Generated code can accidentally drift from runtime expectations** → Add a test comparing `ParamSpecFor` for every built-in driver with the schema-derived expectations, plus negative validation cases.
- **Dynamic drivers are not in the schema** → Keep the runtime registration fallback and test it with `scopedfs` and a fake driver.

## Migration Plan

1. Move the schema to `schema/qrypt.schema.json`; update scripts, docs references, and the config template.
2. Add `x-driver`, `x-i18n`, and `x-param-type` to the schema; add the schema completeness checker.
3. Update the doc generators to read schema metadata only; delete Python metadata/translation tables; regenerate docs.
4. Add `ParamSpec`, the schema-to-Go generator, and the generated map in `pkg/drive`; keep `ParamSchema` compatibility.
5. Update `ValidateMount` to enforce `Required` and `AnyOf`.
6. Remove built-in `ParamDef` literals from driver `init()` functions; keep dynamic registration.
7. Add CI gates for schema validation, generated Go freshness, and generated docs freshness.
8. Verify `qrypt config init`, `qrypt config show`, `qrypt driver schema`, and config validation against a representative config.

Rollback: the change is split so that docs can be reverted independently from runtime generation. If the generated runtime path causes problems, restore `ParamDef` registration from the previous commit while keeping the schema and docs changes; the schema remains valid for editors throughout.
