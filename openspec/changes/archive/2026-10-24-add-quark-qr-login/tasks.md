## 1. Provider-neutral login contract

- [x] 1.1 Add `LoginProvider`, `LoginUI`, `LoginResult`, and `RegisterLoginProvider`/`LoginProviderFor`/`LoginProviderNames` in `pkg/drive/login.go`, keeping the registry independent of the driver factory; verify a fake provider registers, is found by name, is listed in sorted order, and an unknown name reports false
- [x] 1.2 Pin the contract with package tests: `LoginUI.ShowChallenge` may be called more than once, the provider receives the caller's timeout through the context, and `Account(params)` returning `ok=false` means "unknown" so callers fall through to derived naming; verify with `go test ./pkg/drive`
- [x] 1.3 Add `core.ResolveCredentialAccount(cfg, layout, name, provider)` returning `CredentialAccountInfo{Account, Source, Version, UpdatedAt}`, resolving identity as static params → session account and metadata as session → config, reading the session even when the params identify the account; allow a nil provider for drivers without login; verify all four combinations (static+session, session only, static only, neither), the static-account-with-state-metadata case, and nil-provider fallback with `go test ./pkg/core`

## 2. Quark QR login flow

- [x] 2.1 Record the pinned contract in test fixtures from `imeiming/quark-drive` commit `6976f165ee2f3bc19ec1496276d13723803f4722` (`references/auth.md`, `scripts/quark_cli.py`): token success body, service-ticket success body, every status code, and the `Set-Cookie`/redirect cases; verify the fixtures are embedded literals, not network calls
- [x] 2.2 Add `pkg/drivers/quark/login.go` implementing the QR-token request and challenge-URL construction with injectable UOP/pan base URLs and poll interval; verify an `httptest` case asserts `client_id=532`, `v=1.2`, a request id, and the exact `su.quark.cn/4_eMHBJ` query shape
- [x] 2.3 Implement the service-ticket poll state machine mapping `2000000`/`50004001`/`50004002`/`50004003`/`50004004` to success, wait, expiry, failure, and cancellation; verify each status has a test, that expiry reissues the challenge up to a fixed limit, and that a second `ShowChallenge` carries the new token
- [x] 2.4 Exchange the service ticket at `pan.quark.cn/account/info` with a cookie jar that follows redirects, and collect every `quark.cn`/`.quark.cn` cookie into one `k=v; k=v` value while excluding unrelated domains; verify a fake server that sets several cookies across a 302 yields the complete header (set equality, not jar order)
- [x] 2.5 Verify the acquired cookie and derive the account by building the driver without a state store, running `Init`, then `Drop`, and reading `__uid`; verify a cookie that fails `/file/sort` returns an error and no result
- [x] 2.6 Implement `Account(params)` over the stored cookie and register the provider from `init()`; verify `drive.LoginProviderFor("quark")` resolves and account extraction handles missing/malformed `__uid`
- [x] 2.7 Make cancellation and deadline observable: respect context cancellation during polling and return an error that omits the cookie and the token-bearing URL; verify with a context cancelled mid-poll and a timeout test
- [x] 2.8 Keep production endpoints as defaults and pass test endpoints through options, with no test-only branches in the flow; verify the package tests never contact the network

## 3. Transactional persistence

- [x] 3.1 Implement target resolution: `--name` plus same account updates, `--name` plus a different account fails, `--name` plus an unresolvable existing account updates with an explicit-override warning (never worded as confirmed reuse), no `--name` plus a matching account updates, otherwise create the derived free name; re-derive the account from `result.Params` and reject a result whose `Account` contradicts it; verify a table test covers all cases, the unknown-account override warning, the no-silent-reuse case, the inconsistency case, and the unknown-account fallback, all before any save
- [x] 3.2 Build the credential and optional mount on one candidate for an existing config: construct a fresh `CredentialConfig` with a copied `Params` map, apply both upserts inside a single `cliutil.SaveConfig` call behind a replaceable `saveExistingConfig` seam, and count invocations; verify exactly one validated write happens, the seam receives both entries, and the loaded config's maps are not mutated on failure
- [x] 3.3 Add `cliutil.SaveNewConfig(path, candidate)` (validate then `config.Save`) behind a replaceable `saveNewConfig` seam and use it for the no-config bootstrap with `version = "1"`, the mount point, the credential, and one mount (`root_path = "/"`); verify one write, file mode 0600, parent mode 0700, and `config.Validate` accepts the result
- [x] 3.4 Add the pre-login checks: an explicit `--config` that does not load fails with the path before any provider call and creates no directory/file; no config plus non-interactive plus no mount flag fails before the provider call and names both remedies; verify both with a fake provider that records whether `Login` ran
- [x] 3.5 Verify the transactional guarantee for non-write failures: a failing wizard answer, a mount-name conflict, and an invalid final candidate each leave the previous file byte-for-byte unchanged and call the save seam zero times
- [x] 3.6 Prove the write-failure boundary without relying on OS quirks: inject a write error through the save seams and assert the old bytes are unchanged and the credential is absent; add a real unwritable-parent test (skipped as root/Windows) that asserts no temporary file remains
- [x] 3.7 Ensure no secret reaches output: the cookie never appears in stdout, stderr, or logs, `credential show` still masks it, and errors include no token-bearing URL; verify with captured output assertions on success and failure paths
- [x] 3.8 Make `credential list` and `credential show` use `core.ResolveCredentialAccount` with the credential's login provider when one exists and nil otherwise: keep the account static-first while showing `state` plus session version/updated whenever a session exists, show `config` with no session metadata for a credential-only login, and leave unknown fields as `-`; verify a credential-only login is visible with its account and masked secret, a static-account-plus-session credential shows `state` metadata, two credential-only logins for one account warn, a legacy driver without login support still lists its session, and a mounted credential switches to session metadata

## 4. CLI command, mount matrix, and wizard

- [x] 4.1 Add `newLoginCmd` under `internal/cli/credential` with `--type` required, `--name`, `--timeout`, `--no-input`, `--mount-name`, `--root-path`, and `--mount-point`, plus a short, long description, and example; verify the help contract test passes and `qrypt credential login` without `--type` names the flag and the login-capable providers
- [x] 4.2 Select the provider through `drive.LoginProviderFor` and report unsupported types with the available provider list; verify `--type localfs` fails before any provider call
- [x] 4.3 Implement the CLI `LoginUI`: always print the challenge URL, render a QR with `skip2/go-qrcode` only when stdout is a character device, and treat rendering errors as non-fatal; verify with a TTY seam that both branches are covered
- [x] 4.4 Derive the login context from the signal-aware shutdown context and `--timeout`; verify Ctrl+C and timeout paths exit without writing a credential
- [x] 4.5 Implement the post-login wizard with defaults (mount name derived from the credential, `root_path = "/"`, existing effective `mount_point` or `~/Qrypt`), accepting flags in place of prompts; verify scripted stdin covers empty input, explicit values, and flag overrides
- [x] 4.6 Implement the non-interactive matrix: existing config and no mount flag saves only the credential and prints `qrypt mount add <name> --credential <cred> --param root_path=/`; any single mount flag creates the mount with defaults for the rest; verify each branch with no prompt bytes read
- [x] 4.7 Implement fresh-setup behavior: interactive bootstraps through the wizard; non-interactive with any mount flag bootstraps from flags/defaults; non-interactive with no flag fails before login with both remedies; verify all three in `$HOME`-isolated tests
- [x] 4.8 Export the reserved-mount-name predicate from `internal/cli/mount` and use it when deriving a default mount name along with existing mount names; verify `add`/`set`/`remove`/`list` never become derived defaults and an explicit collision fails
- [x] 4.9 Print the final summary (config path, credential name/account, and `qrypt mount` as the next step), or the credential-only next step; verify the strings are localized and contain no secret

## 5. Localization, documentation, and dependencies

- [x] 5.1 Add the new command and output strings to `schema/i18n/cli.en.json` and `schema/i18n/cli.zh-CN.json`, run `python3 scripts/schema/gen-cli-i18n.py`, and commit the regenerated catalog; verify the i18n coverage test passes and no key is untranslated
- [x] 5.2 Update the `credential add` long description to point to `credential login --type quark` for QR login, in both languages, and regenerate; verify the help output mentions the new flow
- [x] 5.3 Document the user flow (scan, credential saved, optional mount, `qrypt mount`) in `docs/for-user`, including the non-interactive matrix and the fresh-setup rule, and link it from the quickstart entry point; verify relative links and that generated docs show no drift
- [x] 5.4 Promote `github.com/skip2/go-qrcode` to a direct requirement and run `go mod tidy`; verify no unrelated module changes and the build still passes

## 6. Verification and local CI

- [x] 6.1 Format every changed Go file with the module toolchain's `gofmt` and confirm `gofmt -l` is empty for them
- [x] 6.2 Run the touched package tests with the race detector (`pkg/drive`, `pkg/core`, `pkg/drivers/quark`, `internal/cli/credential`) and confirm they pass
- [x] 6.3 Run `scripts/ci/check-all.sh` with the Go version pinned in `.github/workflows/ci.yaml` and confirm it exits 0 with `== local CI check passed ==`
- [x] 6.4 Manually smoke-test against a real Quark account: scan the QR, confirm the credential is listed with its account and masked secret, complete the wizard, and mount with `qrypt mount`; verified 2026-10-24: QR login succeeded, `credential list/show` reported the account with a masked cookie (`source: state`), the wizard created mount `quark-all`, and `qrypt mount` is active on `~/Qrypt` with `quark-all` visible
