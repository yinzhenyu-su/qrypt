## Context

See `proposal.md` — Why, and `specs/credential-login/spec.md` for the behavior contract. The current
shape of the code constrains the approach:

- `pkg/drive` has one registry (`Register`/`New`, `pkg/drive/drive.go:446-490`) that only constructs a
  `Driver` from params. No CLI package imports a concrete driver; `pkg/drivers/all` imports them for
  registration side effects, so a CLI login command cannot call `quark.Login` directly without
  breaking that boundary.
- `internal/cli/credential` is the credential lifecycle group. Its subcommands take a credential name
  (or a mount name for `adopt`/`migrate`) and use `--type` for the driver; `add`/`set` already mutate
  `[[credentials]]` through `cliutil.SaveConfig`.
- `cliutil.SaveConfig` (`internal/cli/cliutil/cliutil.go:108-125`) copies, mutates, validates, and
  saves **immediately**. It is safe to call once per command, but calling it once for the credential
  and again for the mount would expose a partially applied login if the second call failed.
- `config.Save` writes atomically with mode 0600 and creates parents with 0700
  (`pkg/config/config.go:225-251`). `config.Validate` requires at least one mount and rejects unknown
  or missing credential params (`pkg/config/validation.go:75-84`), so a credential-only config is not
  a valid file today.
- Config discovery is `--config`, then `./qrypt.toml`, then `~/.qrypt/qrypt.toml`, then the platform
  config directory (`internal/cli/config_path.go:136-160`). `prepareConfig`
  (`internal/cli/config_path.go:48-76`) returns an error when an explicit `--config` file cannot be
  loaded; `Cfg == nil` is only reachable when no path was discovered or supplied. This was verified
  against the built binary: `qrypt credential list --config <missing>` fails with
  `load config ...: no such file or directory`.
- `qrypt mount add NAME --credential NAME --param root_path=...` already exists
  (`internal/cli/mount/mount.go:144`), requires an existing config, and reserves the mount names
  `add`, `set`, `remove`, and `list`.
- Shared credentials already give a login result a home: a mount that references a credential merges
  its params, and the Quark driver commits the config cookie into the shared session on the first
  successful `Init` (`pkg/drivers/quark/driver.go:89-129`). No extra session write is needed.
- Interactive prompts already have a pattern: `cmd.InOrStdin()` plus a `stdinIsTerminal` seam
  (`internal/cli/receive/options.go:126-135`).
- CLI strings are generated from `schema/i18n/cli.*.json` into
  `internal/cli/i18n/catalog_gen.go`; the help contract test requires a short, long, and example for
  every leaf command.
- `github.com/skip2/go-qrcode` is already in the module graph as an indirect dependency.

## Goals / Non-Goals

**Goals:**

- One provider-neutral way for the CLI to start an interactive login and receive credential-scope
  params plus an account identity.
- A Quark implementation whose protocol is testable without the real service.
- One in-memory candidate configuration and exactly one validate-and-write step, so no failure path
  can leave a credential or mount behind.
- Login output that lands in the existing shared-credential model and is immediately mountable.
- A first-run path that takes a user from "nothing configured" to a mountable config without
  inventing a config file in the current directory.

**Non-Goals:**

- No new top-level `auth` command group.
- No logout, session inspection, or token refresh commands; `credential remove/show/list` cover
  lifecycle visibility, and driver sessions still own refresh.
- No browser-redirect or device-code login; the provider contract is kept small and extended when a
  second method appears.
- No encryption setup in the wizard; encryption stays a separate opt-in config section.
- No automatic merge of two already-declared credentials that resolve to the same account; login only
  reuses or updates one, matching the explicit-merge rule in `credential-lifecycle`.
- No config-file comment preservation. Existing mutating commands already normalize the file through
  TOML marshal; login follows that behavior instead of adding a second writer.
- No relaxation of `config.Validate` to allow a credential-only configuration.

## Decisions

### D1. `credential login --type <driver>` instead of a new `auth` group

The artifact of login is a `[[credentials]]` entry, and the group already owns authentication actions
(`credential test`) and lifecycle operations. A separate `auth` group would immediately duplicate
`remove`, `list`, `show`, and `test` under new names for the only flow that exists today. Keeping
positional arguments as credential names requires the provider to be a flag, which also matches
`credential add --type`.

- Alternative: `qrypt auth login quark`, as originally sketched in the issue. Rejected for now — it
  introduces a second vocabulary for one flow and would make `auth logout/status/refresh` duplicate
  `credential remove/list/show/test`.
- Alternative: `qrypt credential login quark` with the provider as a positional argument. Rejected —
  `credential show quark` and `credential login quark` would interpret the same word differently.
- Reversal cost is low: the provider contract and persistence helpers live outside the command file,
  so moving the wiring later is a small change.

### D2. Provider-neutral login contract in `pkg/drive`

Add a second, independent registry rather than overloading the driver factory, because constructing a
`Driver` requires the secret that login is supposed to obtain.

```go
// A LoginProvider acquires credential-scope params through an out-of-band flow.
type LoginProvider interface {
    // Login runs the flow until it succeeds, the context is done, or it fails.
    Login(ctx context.Context, ui LoginUI) (LoginResult, error)
    // Account reports the account identity already encoded in params, when it
    // can be derived offline. ok=false means "unknown", not "no account".
    Account(params Params) (account string, ok bool)
}

type LoginUI interface {
    // ShowChallenge presents a login URL. It may be called again when a
    // challenge expires and is reissued.
    ShowChallenge(url string) error
}

type LoginResult struct {
    Params  Params
    Account string
}

func RegisterLoginProvider(name string, provider LoginProvider)
func LoginProviderFor(name string) (LoginProvider, bool)
func LoginProviderNames() []string
```

- Alternative: a CLI type switch importing `pkg/drivers/quark`. Rejected — it couples command
  packages to a provider and bypasses registration.
- Alternative: extend `Factory`/`Driver` with login. Rejected — the driver cannot be built before the
  credential exists.
- The UI callback only carries the challenge URL. Rendering and localization stay in the CLI; the
  provider never decides how output looks.

### D3. Quark login is a standalone flow in the driver package with injectable endpoints

`pkg/drivers/quark/login.go` implements `drive.LoginProvider` and registers itself in the existing
`init()`. It uses a dedicated `net/http` client with a cookie jar, not the driver's authenticated
`client`:

1. `GET {uop}/cas/ajax/getTokenForQrcodeLogin?client_id=532&v=1.2&request_id=<uuid>`
2. `GET {uop}/cas/ajax/getServiceTicketByQrcodeToken?client_id=532&v=1.2&token=<token>&request_id=<uuid>`
   every two seconds until success, expiry, failure, cancellation, or the context deadline.
3. `GET {pan}/account/info?st=<ticket>&lw=scan`, following redirects, then collect the `quark.cn`
   cookies from the jar into a `k=v; k=v` header value.
4. Verify and derive the account: build the driver with the cookie (`New(cookie, Options{})`) and run
   `Init` without a state store, then `Drop`. `Init` performs the same `/file/sort` validation a mount
   would; `cookieAccount` reads `__uid` from the cookie.

The UOP and pan base URLs and the poll interval are options with production defaults so tests can
point them at `httptest.Server`.

- Alternative: poll in the CLI. Rejected — it leaks the provider state machine into the command and
  duplicates it for every provider.
- Alternative: verify with a hand-written request. Rejected — `Init` is the exact path a mount takes.

#### External protocol contract (pinned)

The flow is undocumented and must not be treated as stable API. Pin it to
`imeiming/quark-drive` commit `6976f165ee2f3bc19ec1496276d13723803f4722` (2026-05-11):

- `references/auth.md`, blob `811c75d57189a46cd5b6174c81457f1b089f3c67`
- `scripts/quark_cli.py`, blob `aac9e84eb4b598a2918181bd117514fa972e2eb4`

The contract the implementation and tests must satisfy:

| Request | Query/body | Success shape | Notes |
| --- | --- | --- | --- |
| `GET {uop}/cas/ajax/getTokenForQrcodeLogin` | `client_id=532`, `v=1.2`, `request_id=<uuid4>` | `{"status":2000000,"message":"ok","data":{"members":{"token":"..."}}}` | any other status is an error |
| `GET {uop}/cas/ajax/getServiceTicketByQrcodeToken` | `client_id=532`, `v=1.2`, `token`, `request_id` | `{"status":2000000,"message":"ok","data":{"members":{"service_ticket":"..."}}}` | poll every 2s |
| `GET {pan}/account/info` | `st=<service_ticket>`, `lw=scan` | HTML/JSON response plus `Set-Cookie` headers, possibly across a redirect | cookie jar collects the header value |

Status mapping: `2000000` success, `50004001` waiting for scan, `50004002` QR expired (reissue,
bounded), `50004003` login failed, `50004004` login cancelled.

QR payload: `https://su.quark.cn/4_eMHBJ?token=<token>&client_id=532&ssb=weblogin&uc_param_str=&uc_biz_str=S:custom|OPT:SAREA@0|OPT:IMMERSIVE@1|OPT:BACK_BTN_STYLE@0`.

Cookie rules: after the account-info request completes (including redirects), collect every cookie
whose domain is `quark.cn` or ends in `.quark.cn`; host-only `pan.quark.cn` cookies qualify; cookies
for unrelated domains are excluded; duplicate names keep the jar's final value. Tests assert these
rules with literal fixtures and set equality rather than relying on jar iteration order.

### D4. Persist as credential params, not as a session file

The login result becomes `[[credentials]]` with `type = <driver>` and the returned credential-scope
params (`cookie` for Quark). The first mount that references it commits the cookie into the shared
session through the existing `Init` path. Writing the session directly would duplicate the manager's
fingerprint, locking, and version rules and would make login depend on the runtime state layout.

- Alternative: write `state/credentials/<name>/...` from the CLI. Rejected — a second owner of the
  session lifecycle, and it must guess the storage layout before Core exists.
- Consequence: a login without any mount leaves the credential only in config; it still validates and
  is observable through the account resolver (D5), and the session appears on first use.

### D5. Separate account identity from session metadata in one resolver

Account identity and display metadata have different precedence, so they must be separate fields of
one result rather than two values returned by an early-returning helper:

```go
// CredentialAccountInfo is the offline identity and display metadata of one
// credential. Identity and session metadata are independent: Account may come
// from static params while Source/Version/UpdatedAt describe a session.
type CredentialAccountInfo struct {
    Account   string    // static params first, session fallback; empty when unknown
    Source    string    // "state" when a session exists, "config" when only static params identify it, empty when neither
    Version   uint64    // session only, zero otherwise
    UpdatedAt time.Time // session only, zero otherwise
}

// ResolveCredentialAccount never contacts the network. provider may be nil for
// drivers that do not implement login; such credentials fall back to session
// metadata or report unknown.
func ResolveCredentialAccount(cfg *config.Config, layout RuntimeLayout, name string, provider drive.LoginProvider) CredentialAccountInfo
```

Algorithm:

1. Look up the credential in the config; missing → zero result.
2. If `provider != nil`, `account, known = provider.Account(cred.Params)`.
3. Always read session metadata: `session, hasSession = CredentialSessionState(cfg, layout, name)`.
   This read is not skipped when step 2 succeeded, so session source/version/updated stay available.
4. If the params could not identify the account and the session has one, use the session account.
5. Identity: the static account wins when known; the session account is the fallback.
6. Display metadata: when a session exists, `Source = session.Source` (`state`) and
   `Version`/`UpdatedAt` come from the session even if the account came from static params. With no
   session, a known static account sets `Source = config`; otherwise `Source` stays empty and all
   metadata is unknown.

Target selection then follows, using `info.Account` only:

1. `--name` given and the credential exists with the same account → update its params.
2. `--name` given and the account differs → fail with the conflicting account, no write.
3. No `--name` and any credential of the same type resolves to this account → update that credential.
4. Otherwise create `<type>`, or `<type>-2`, `<type>-3`, ... when the name is taken by another account.

When the account is unknown on both sides, same-account reuse and cross-account conflict detection are
skipped: an explicit `--name` is updated as requested and an omitted name derives a free one. An
explicit `--name` that targets a credential whose existing account cannot be resolved is treated as a
deliberate override: the command updates it but prints a warning that the existing credential's
account could not be verified, and it must not word the result as a confirmed same-account reuse.

- Alternative: key credentials by account id. Rejected — account is discovered only at runtime and is
  already documented as metadata, not identity (`credential-lifecycle` D2).
- Alternative: always create a new credential. Rejected — it would defeat the shared lifecycle and
  produce the duplicate-account warning immediately.

The account reported by a login result is not trusted on its own. After verification the CLI calls
`provider.Account(result.Params)` again and treats that derivation as authoritative. If both exist and
disagree, the login fails before any config mutation; if the derivation is unavailable, the provider's
reported account is used as-is.

`credential list` and `credential show` must use this resolver instead of reading only
`CredentialSessionState` (`internal/cli/credential/credential.go:110-131`), otherwise a credential-only
login has no visible account:

- `ACCOUNT` is `info.Account` when known, `-` otherwise.
- `SOURCE` is `info.Source` (`state` whenever a session exists, `config` for a static-only login), `-`
  when neither is known.
- `VERSION` and `UPDATED` are printed only when `SOURCE = state`; otherwise `-`.
- `credential show` always prints `account`, `source`, `version`, and `updated_at`; version and
  updated reflect the session only for `source: state`, and every unknown value is printed as `-`.
- The duplicate-account warning in `credential list` uses `info.Account`, so two credential-only
  logins for the same account are flagged even before either has a session.
- Drivers without login support pass a nil provider: the resolver still reads the session, so their
  existing list/show output does not regress and never panics.

The mismatch case (static account known and different from the session account) keeps the static
account as identity and still reports `source: state`: replacing a credential's secret is what makes a
session stale, and the params fingerprint normally discards such a session before it can be observed
(`credential-lifecycle`, "Session reuse is bound to the credential parameters").

- Alternative: have the CLI call `CredentialSessionState` inline. Rejected — it is the same path
  assembly the helper exists to prevent, and precedence would drift between commands.
- Alternative: put the resolver in `pkg/drive`. Rejected — `drive` must not depend on `pkg/config` or
  the runtime storage layout.
- Alternative: persist the account in the credential config. Rejected — it extends the config schema
  and creates a second source of truth that goes stale when the secret is replaced.
- Alternative: return `(account, source, ok)` and short-circuit once the params identify the account.
  Rejected — it cannot carry session version/updated and reintroduces the credential-only
  observability gap this section exists to close.

### D6. One candidate, one validate, one atomic write

The command never persists the credential before the mount decision. It builds the whole result in
memory and saves once:

1. Resolve the target path before contacting the provider. An explicit `--config` must load (existing
   `prepareConfig` behavior); no discovered path means the home default. Pre-check the non-interactive
   no-config/no-flags case here, so the user is not asked to scan for a flow that cannot be saved.
2. Run login and verification.
3. Resolve the credential on a candidate built from the loaded config. `SaveConfig` only copies the
   credential/mount slices, so the command MUST build a fresh `CredentialConfig` with a copied
   `Params` map instead of mutating the loaded entry in place.
4. Collect mount answers or flags. Every conflict and validation check happens here, before any write.
5. Apply exactly one save:
   - Existing config: one `cliutil.SaveConfig(state, func(candidate){ credential upsert; optional
     mount upsert; optional global mount point })` call, which validates the candidate and writes once.
   - No config: build the candidate from empty, validate it, and call `config.Save` once through a new
     `cliutil.SaveNewConfig(path, candidate)` helper that mirrors `SaveConfig`'s validate-then-save
     order. Do not call two mutation commands or two save APIs.

Both save paths sit behind replaceable package-level seams (`saveExistingConfig`, `saveNewConfig`) so
tests can count invocations and inject a failure at the validation or write stage; the defaults
delegate to `cliutil`. A real-filesystem failure test uses an unwritable parent directory where the OS
enforces permissions (skipped as root and on Windows) and asserts the previous file is unchanged with
no temporary file left behind.

If any step returns before the commit point, the file is untouched. There is no rollback path because
there is no intermediate write. The atomic rename is the commit point: cancellation is checked
immediately before it, and a cancellation observed after that final check does not roll back a
committed write.

- Alternative: call `SaveConfig` for the credential and again for the mount. Rejected — the first call
  persists a credential even when the wizard or mount validation later fails, violating the
  transactional requirement.
- Alternative: save the credential, then offer the wizard on a best-effort basis. Rejected for the
  same reason; the issue's flow treats login and mount setup as one operation.
- A failed final write leaves the old file intact because `config.Save` writes a temporary file and
  renames it (`pkg/config/config.go:225-251`).

### D7. Config target: explicit, discovered, then the home default

- `--config PATH` keeps today's behavior: `prepareConfig` loads it and fails when it does not exist.
  Login adds no fallback for a missing explicit path, creates no parent directory, and surfaces the
  existing `load config "<path>": ...` error before the QR challenge.
- Without `--config`, the discovered file (`./qrypt.toml`, `~/.qrypt/qrypt.toml`, or the platform
  config directory) is used.
- When nothing is discovered and a config is required, the command bootstraps
  `~/.qrypt/qrypt.toml`: `version = "1"`, `[mount] mount_point` (flag or `~/Qrypt`), the credential,
  and one mount (`root_path = "/"`). `config.Save` creates the parent with 0700 and the file with
  0600.
- The command always prints the path it wrote. Existing handlers keep their behavior; login does not
  silently relocate a project config.

- Alternative: require a prior `qrypt config init`. Rejected — it adds a step to the issue's flow and
  creates an unrelated `localfs` mount at a project-relative path.
- Alternative: relax `ValidateGlobal` to allow a credential-only config. Rejected — it changes the
  config contract for every command for one edge case.

### D8. Mount setup matrix and defaults

| Config exists | Mount flags | Interactive | Result |
| --- | --- | --- | --- |
| yes | none | yes | wizard asks for name/root path/mount point, then adds the mount |
| yes | none | no | credential only; print `qrypt mount add <name> --credential <cred> --param root_path=/` |
| yes | any | either | non-interactive mount creation from flags plus defaults |
| no | none | yes | bootstrap config with the wizard's mount |
| no | any | either | bootstrap config with the flag-driven mount |
| no | none | no | fail before the provider flow, naming the mount flags and the interactive alternative |

Any single mount flag switches to non-interactive mount creation; the others fall back to defaults.
Defaults: mount name derived from the credential name, skipping existing mounts and the reserved names
`add`, `set`, `remove`, `list` (`internal/cli/mount/mount.go:23-30`); root path `/`; mount point from
the effective config, else `~/Qrypt`. A user-supplied `--mount-name` that already exists is an error,
not an overwrite, matching `qrypt mount add`.

`--mount-point` sets the namespace root (`[mount].mount_point`); when the legacy top-level
`mount_point` is populated, both fields are kept in agreement. The command explains that a mount
appears as `<mount point>/<mount name>`.

- Alternative: allow a partial flag set to prompt for the rest. Rejected — non-interactive mode must
  never read stdin, and a mixed mode is hard to script.
- Alternative: derive a unique mount name silently even for an explicit `--mount-name`. Rejected —
  explicit names must fail loudly so a typo never creates a second mount.

### D9. Rendering and secret handling

The CLI implements `drive.LoginUI`. It prints the challenge URL, and when stdout is a character device
it also prints a QR code using `skip2/go-qrcode` (`ToSmallString`). QR rendering failure is not fatal:
the URL remains. The cookie is only held in memory between verification and the final save; it is
never included in errors, progress text, or logs, and `credential show` keeps masking it. The
token-bearing URL is printed only as the challenge and never repeated in error messages.

- Alternative: rely on the URL only. Rejected — scanning is the flow the issue asks for; a URL is a
  fallback, not the primary experience.

## Risks / Trade-offs

- **Undocumented CAS endpoints drift** → Pin the source commit and replicate its request/response
  contract in `httptest` fixtures; verification via `Init` catches a cookie that exchanges but does
  not authenticate. Manual `credential add --param cookie=...` remains the documented fallback.
- **Cookie extraction depends on redirects and cookie domains** → Use a cookie jar, apply the
  `quark.cn` domain rule, and test multiple `Set-Cookie` headers across a redirect with set equality.
- **Transaction failure after a successful scan forces a re-scan** → Accepted; the alternative is a
  persisted credential the user did not finish configuring. The command says nothing was saved.
- **A fresh, non-interactive run without mount flags fails before scanning** → Deliberate: a
  credential-only config is invalid today. The error names both remedies (`--mount-name`/`--mount-point`
  or run interactively), so no time is wasted on a QR that cannot be saved.
- **Rewriting an existing config drops comments and formatting** → Same behavior as `credential
  add/set`; validate-then-save preserves semantics and writes atomically. The command prints the path.
- **Writing `~/.qrypt/qrypt.toml` when the user expected a project config** → Discovery order is
  unchanged and the home file is only created when discovery finds nothing; `--config` always wins
  and the chosen path is printed.
- **Token-bearing URL in terminal scrollback** → The token is short-lived and single-purpose; it is
  never logged or echoed in errors. Rendering it is the only way to complete a headless login.
- **A new direct dependency** → `skip2/go-qrcode` is pure Go and already present transitively; it is
  used only by the CLI, never by drivers or the mobile API.
- **Reserved mount names collide with derived defaults** → Derivation checks the reserved set and
  existing mounts; only explicit `--mount-name` collisions fail. Export the reserved-name predicate
  from the mount command package so the two lists cannot drift.

## Migration Plan

- Additive. Existing configs, credentials, and sessions are untouched; inline-cookie mounts keep
  working and manual `credential add` stays available.
- Rollback is deleting the new subcommand and its strings; no persisted data needs reverting. A config
  bootstrap performed by login is a normal config that older builds already understand.
- No state-directory migration is needed because login writes only config params.

## Open Questions

None that affect the specs or the task breakdown. The exact cookie names Quark requires for every API
surface, and whether any endpoint needs a header beyond the cookie, will be confirmed by the
verification step and contract tests during implementation; a gap there fails the login before any
config is written.
