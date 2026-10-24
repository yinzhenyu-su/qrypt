## 1. Configuration-level credential sharing

- [x] 1.1 Add `CredentialConfig{Name, Type, Params}` to `pkg/config` plus `MountConfig.Credential`, and confirm `Load`/`Save` round-trips the new section without touching existing mount entries; verify with a config fixture that writes, reloads, and compares both credentials and mounts
- [x] 1.2 Enforce credential validation in `pkg/config/validation.go`: unique names, single path component, registered type, mount type equality, unknown reference, and rejection of `credential` combined with inline credential-scope params; verify each rejection with a table test
- [x] 1.3 Add `x-param-scope` (`credential` / `mount` / `both`) to every built-in driver parameter in `schema/qrypt.schema.json`; verify `scripts/schema/check-schema.py` rejects a built-in parameter without a scope
- [x] 1.4 Generate `drive.ParamDef.Scope` from the schema and expose it through `ParamSpecFor`; verify generated metadata reports the same scope as the schema and that generation is idempotent
- [x] 1.5 Make mount validation scope-aware: credential-scope requirements checked on the credential, mount-scope requirements on the mount, `both` accepted on either with mount precedence, out-of-scope params rejected; verify with fixtures for each case
- [x] 1.6 Merge credential params with mount params in `pkg/core.buildMount` before `drive.New`, and synthesize an implicit `mount:<name>` credential for legacy inline mounts that keeps `state/driver/<mount>`; verify a two-mount shared credential passes identical merged params apart from `root_path`, and a legacy config produces unchanged params
- [x] 1.7 Regenerate the CLI i18n catalog and user docs (`gen-cli-i18n.py`, `gen-driver-docs.py`, `gen-config-docs.py`) and confirm `scripts/ci/check-all.sh` reports no generated drift
- [x] 1.8 Add `credential` to `core.ConfigSummary` mounts plus a masked `credentials` list, and add credential actions to `ConfigUpdateRequest`; verify secrets render as `***` and an update preserves an unmentioned secret value
- [x] 1.9 Add `qrypt credential add|list|show|set|remove` and `qrypt mount add --credential`; verify a credential-only edit changes the effective params of every referencing mount

## 2. Shared runtime session

- [x] 2.1 Add `drive.CredentialSession` and `drive.CredentialSessionInstaller` with opaque payloads, versioned `Update`, `Subscribe`, and `Refresh`; verify no provider-specific field appears in `pkg/drive`
- [x] 2.2 Implement `pkg/core/internal/credentials`: session object with mutex, version counter, subscriber fan-out, atomic 0600 persistence, `flock` guard, and version-conflict retry; verify with concurrent commit tests and a simulated second process
- [x] 2.3 Add a process-wide manager keyed by absolute state directory plus credential name, acquired by `pkg/core` before mounts open and reused across `Core.Reload`; verify two Cores in one process share one session object
- [x] 2.4 Persist a params fingerprint with each session and discard the session when it changes; verify replacing a credential's cookie discards it for all mounts while a `root_path` change keeps it
- [x] 2.5 Migrate the Quark driver to the shared session for `quark_cookie.json`: read the payload at `Init`, commit rotations through `Update`, follow siblings through `Subscribe`, and drop the per-mount seed/account guard now handled by the manager; verify two Quark mounts rotate once and both observe the new cookie
- [x] 2.6 Migrate the remaining cookie drivers (`115`, `189`, `yun139`) to the shared session; verify each driver's rotation test asserts propagation to a sibling mount and isolation from a second credential
- [x] 2.7 Add an `ErrAuth` recovery helper that re-reads the session snapshot, retries once, then re-authenticates from static params; verify a sibling rotation unblocks an auth failure without a restart and a genuine auth failure still surfaces
- [x] 2.8 Make credential fields observable: `name`, `account`, `source`, `version`, `updated_at`, `used_by` on the control surface, and credential name/source/version in per-mount debug snapshots; verify no secret value appears in either output

## 3. Refresh coalescing and migration

- [x] 3.1 Implement `Refresh` single-flight in the credential manager and pin its semantics with tests: one provider call for N concurrent mounts, shared result, and no session write on failure
- [x] 3.2 Migrate token drivers `aliyundrive`, `p123`, `p115open`, `baidunetdisk`, `onedrive`, `onedrive_app` to the shared session and coalesced refresh; verify a rotated refresh token is committed for all mounts and only refreshed once
- [x] 3.3 Add `qrypt credential migrate`: adopt a legacy `state/driver/<mount>/` session only when its account matches the credential, keep the legacy file by default, and delete it only with an explicit flag; verify matching, mismatched, and repeated migration runs
- [x] 3.4 Warn when two credentials of the same driver type resolve to the same account, naming both credentials and their mounts; verify the warning appears in `credential list` and logs without exposing secrets
- [x] 3.5 Register the new package and allowed imports in `scripts/ci/checkarch`, and update `docs/for-developer/architecture.md`, `package-boundaries.md`, and the user docs to describe shared credentials; verify `scripts/ci/check-arch.sh` passes
- [x] 3.6 Run the full local gate (`scripts/ci/check-all.sh`) and confirm it ends with `== local CI check passed ==`; verify the new manager packages are covered by unit and race tests
