# Schema

`schema/qrypt.schema.json` is the single source for qrypt's declarative driver
parameter metadata and its user-facing translations.

## Layout

```text
schema/
  qrypt.schema.json
  README.md
```

The schema is an input. It feeds editor completion, user documentation and the
generated Go parameter metadata. Files under `docs/` are outputs and must not
be used to edit parameter definitions.

CLI message catalogs live next to the schema:

```text
schema/i18n/cli.en.json
schema/i18n/cli.zh-CN.json
```

They are keyed by command/flag path and rendered output keys. The two files
must contain the same keys. Every non-example `zh-CN` value must be a real
translation rather than an English copy, translated format strings must keep
the same verbs, and `.example` command snippets must stay identical to the
English commands. Missing values still fall back to English at runtime, but
`scripts/schema/check-cli-i18n.py` rejects missing or untranslated entries in
CI. Update the generated catalog with `scripts/schema/gen-cli-i18n.py`.

## Driver metadata

Every built-in driver has a `*Params` definition with:

- `title`: English display name.
- `x-driver.type`: the runtime driver type used in `[[mounts]].type`.
- `x-driver.order`: order in generated documentation.
- `x-i18n["zh-CN"].title`: localized display name.
- `x-i18n["zh-CN"].intro`: optional localized introduction.

## Parameter metadata

Every user-visible parameter has:

- `description`: English description.
- `x-i18n["zh-CN"].description`: Chinese description.
- `x-secret`: present for values that must be masked.
- `x-example`: example value shown in config templates and docs.
- `x-param-type`: optional runtime type when it differs from the JSON Schema
  type (for example `duration` for S3's `sign_url_expire`).
- `x-doc-default`: optional display default when the machine-readable default
  is not useful to users (for example `<work_dir>/cache/read`).

`required` and `anyOf` describe unconditional and alternative requirements.
`anyOf` groups must only contain the parameters that distinguish the choices;
common required parameters belong in `required`.

## Workflow

After editing the schema:

```sh
python3 scripts/schema/check-schema.py
python3 scripts/schema/gen-drive-schema.py
python3 scripts/schema/check-cli-i18n.py
python3 scripts/schema/gen-cli-i18n.py
python3 scripts/schema/gen-driver-docs.py
python3 scripts/schema/gen-config-docs.py
```

CI fails when the schema is incomplete or when generated Go metadata or user
documentation is stale.

## Adding a driver

1. Add the driver type to `definitions.mountConfig.properties.type.enum`.
2. Add the matching `allOf` `if`/`then` block.
3. Add `definitions.<name>Params` with `title`, `x-driver`, `x-i18n`, properties,
   `required`/`anyOf`, and `additionalProperties: false`.
4. Run the commands above and commit the schema and generated files.
