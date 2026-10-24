## Why

Today the only way to give qrypt a Quark cookie is to copy it out of a browser's developer tools and
paste it into `qrypt credential add --param cookie=...`. The value is long, easy to truncate or leak,
and an expired cookie only surfaces later as an authentication failure during a mount. Quark itself
recommends QR-code login as the normal flow, but qrypt exposes no way to run it.

The shared-credential work (`[[credentials]]` plus one shared session per credential) is now in
place, so a login flow has a natural destination: acquire the cookie once, store it as one named
credential, and let any number of mounts reuse it.

## What Changes

- Add a provider-neutral interactive-login contract in `pkg/drive` so the CLI can offer
  `credential login` without importing a concrete driver, plus a registry lookup by driver type.
- Implement Quark QR-code login in `pkg/drivers/quark`: request a QR token, render the challenge,
  poll for scan status, exchange the service ticket for cookies, verify the cookie, and return the
  credential-scope params plus the account identity.
- Add `qrypt credential login --type <driver>` to the existing `credential` group (no new top-level
  `auth` group). Positional arguments stay credential names; `--type` selects the provider, matching
  `credential add`.
  - `--name` names the credential; when omitted, reuse a credential that already resolves to the same
    account, otherwise derive a name.
  - `--timeout` bounds the wait for a scan.
  - `--no-input` skips the wizard. On an interactive terminal without it, a wizard adds the first
    mount. Non-interactively, supplying any of `--mount-name`, `--root-path`, or `--mount-point`
    creates the mount from those flags plus defaults; with no mount flag and an existing config, only
    the credential is saved.
  - A fresh setup with no config file and no mount flags fails before the QR flow in non-interactive
    mode, because a credential-only config is not valid today.
- Add `credential list` and `credential show` account derivation from stored params when no runtime
  session exists (`source: config`), so a credential-only login is observable without persisting an
  account field.
- Build the credential update and the optional mount on one in-memory candidate, validate the whole
  candidate, and perform exactly one atomic write. Any failure, cancellation, or timeout leaves the
  configuration byte-for-byte unchanged.
- After a successful login, an interactive wizard (TTY only) adds a mount referencing the new
  credential and prints `qrypt mount` as the next step. When no config file exists and a mount is
  being created (by the wizard or by flags), bootstrap `~/.qrypt/qrypt.toml` with the credential and
  that mount.
- Preserve existing config entries, reuse the existing same-account credential instead of creating a
  duplicate, and never print or log the cookie or the token-bearing QR URL.
- Render the QR code in the terminal when possible and always print the URL as a fallback; refresh
  an expired QR token a bounded number of times.
- Localize the new command, document the user flow, and add the QR rendering dependency as a direct
  requirement.

**BREAKING**: none. The command is additive and existing inline-cookie configs keep working.

## Capabilities

### New Capabilities

- `credential-login`: interactive provider login that acquires a credential from the user out of
  band, stores the result as a named shared credential, and optionally creates the first mount.

### Modified Capabilities

- None. The existing `credential-lifecycle` requirements (one shared session, rotation, observability)
  already describe where the acquired credential lives; this change adds acquisition, not a change to
  those requirements. `cli-help` and `cli-localization` requirements already cover any new command.

## Impact

- `pkg/drive`: provider-neutral login contract (`LoginProvider`, `LoginUI`, registry).
- `pkg/core`: `ResolveCredentialAccount` so account lookup (static params, then persisted session)
  lives in one place instead of being re-assembled by the CLI.
- `pkg/drivers/quark`: QR login implementation with injectable base URLs, pinned to the reference
  commit and covered by contract fixtures.
- `internal/cli/credential`: `login` subcommand, in-memory candidate plus one validate-and-save step,
  interactive mount wizard, terminal QR rendering, i18n strings.
- `internal/cli/cliutil`: a validate-then-save helper for a newly bootstrapped config.
- `internal/cli/mount`: export the reserved-mount-name predicate used by default name derivation.
- `internal/cli/command_root.go`: no new group; the subcommand is registered under `credential`.
- `schema/i18n/cli.en.json`, `schema/i18n/cli.zh-CN.json` and the generated catalog.
- `go.mod`: `github.com/skip2/go-qrcode` becomes a direct dependency.
- `docs/for-user`: a login quickstart section and next-step guidance; generated docs stay unchanged.
- Tests: Quark login HTTP contract, credential upsert/reuse, wizard and non-interactive paths, and
  no-secret-output assertions.
