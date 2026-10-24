## Why

Today every `[[mounts]]` entry carries a complete credential. Mounting the same account at several
directories therefore means copying the same secret N times, and each mount persists and rotates its
own session. That produces three concrete failures:

- Rotating credentials (quark `__puus`, 115/189/139 cookies, aliyun/123/115-open/onedrive refresh
  tokens) diverge per mount. A single-use refresh token rotated by one mount invalidates the others.
- A mount's persisted rotation state can silently shadow a replaced credential, so a mount keeps
  serving the previous account. This is the reported `quark-2` / `quark-download` incident: the
  configured cookie was account B while the persisted session still belonged to account A, and the
  stale session validated successfully.
- Credential lifecycle (rotation, refresh, invalidation) has no single owner, so it cannot be
  observed or reasoned about as one thing.

The goal is one credential and one lifecycle per `(driver type, account)`, referenced by any number
of directory mounts, without giving up per-mount isolation of paths, caches, upload sessions, and
offline tasks.

## What Changes

- Add top-level `[[credentials]]` entries (`name`, `type`, `params`) and a `credential` reference on
  mounts. Credential params and mount params are merged before `drive.New`, so drivers keep receiving
  one flat `drive.Params`.
- Split driver parameters by scope (`credential`, `mount`, `both`) in the canonical schema, generated
  metadata, validation, and generated documentation.
- Add a process-wide `CredentialManager` and a `drive.CredentialSession` contract: one shared session
  object per credential, versioned commits, cross-mount subscriptions, coalesced refresh, and atomic
  file persistence under `state/credentials/<name>/`.
- Invalidate a credential session when its static params change, using a params fingerprint instead of
  matching per-mount persisted cookies.
- Keep legacy configs working: a mount with inline params gets an implicit per-mount credential whose
  state stays in `state/driver/<mount>/`; an explicit `qrypt credential migrate` command moves a
  legacy session into the shared credential store.
- Add `qrypt credential add|list|show|set|remove|migrate`, credential fields in the control/mobile
  config summary, and credential lifecycle fields in per-mount debug snapshots.
- Migrate rotating drivers in order: cookie rotation (quark, 115, 189, yun139), then refresh-token
  drivers (aliyun, 123, 115-open, baidu_netdisk, onedrive). Static drivers need no changes.

## Capabilities

### New Capabilities

- `credential-lifecycle`: named, mount-independent credentials with one shared, versioned session per
  credential; explicit scope split between credential and mount parameters; a single owner for
  rotation, refresh, invalidation, persistence, and observability.

### Modified Capabilities

- `driver-parameter-schema`: parameter definitions additionally declare a scope, and generated
  metadata, validation, and documentation enforce where each parameter may appear.

## Impact

- `pkg/config`: `credentials` section, mount `credential` reference, scope-aware validation, legacy
  synthesis.
- `pkg/drive`: `ParamDef.Scope`, `CredentialSession`, `CredentialSessionInstaller`.
- `pkg/core`: process-wide credential manager wiring, merged params at `buildMount`, credential
  summary and debug fields.
- `pkg/core/internal/credentials` (new): session objects, file store with `flock`, refresh
  single-flight, subscriptions.
- `pkg/drivers/quark`, `p115`, `p189`, `yun139`: cookie rotation moves to the shared session;
  `aliyundrive`, `p123`, `p115open`, `baidunetdisk`, `onedrive`: token refresh moves to coalesced
  refresh. `localfs`, `sftp`, `s3`, `webdav` are unchanged.
- `schema/qrypt.schema.json`, `pkg/drive/builtin_schema_gen.go`, `internal/cli/i18n/catalog_gen.go`,
  `docs/for-user/full-config.md`, `docs/for-user/support-drivers.md`: new sections and scopes, all
  regenerated.
- `internal/cli`, `pkg/control`, `pkg/mobile`: credential commands, summary fields, masked secrets.
- `scripts/ci/checkarch`: register the new package and its allowed imports.
- Tests: config validation, manager concurrency, shared rotation across mounts, per-account isolation,
  state migration, legacy compatibility.
