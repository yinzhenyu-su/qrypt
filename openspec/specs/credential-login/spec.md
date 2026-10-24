# credential-login Specification

## Purpose
Let a user obtain a provider credential by completing an out-of-band login (for example scanning a QR
code) instead of copying a secret out of a browser, and store the result as a named shared credential
that any number of mounts can reference.

## Requirements

### Requirement: Provider login lives in the credential group and names its provider

The CLI SHALL expose `qrypt credential login` as a subcommand of `credential`, and the provider MUST
be selected explicitly with `--type <driver>`. Positional arguments of the `credential` group MUST
continue to denote credential names, not driver types. A type without login support, or a missing
type, MUST fail before any provider request and MUST name the providers that do support login.

#### Scenario: Supported provider is selected

- **WHEN** a user runs `qrypt credential login --type quark`
- **THEN** the command starts the Quark login flow and does not treat `quark` as a credential name

#### Scenario: Provider without login support is rejected

- **WHEN** a user runs `qrypt credential login --type localfs`
- **THEN** the command exits with an error naming `localfs` and listing the providers that support login

#### Scenario: Missing provider is reported

- **WHEN** a user runs `qrypt credential login` without `--type`
- **THEN** the command exits with an error that names the required flag and the available login providers

### Requirement: Login challenge is presented without manual secret handling

The command SHALL display the provider's login challenge, including the challenge URL, and MUST render
a scannable form in an interactive terminal when the provider supplies one. It MUST poll the provider
for completion, MUST report progress without exposing secrets, and MUST bound the wait with a
configurable timeout.

#### Scenario: QR challenge is shown

- **WHEN** the Quark login flow starts
- **THEN** the command shows a QR code in the terminal when possible and prints the login URL as a fallback

#### Scenario: Expired challenge is renewed a bounded number of times

- **WHEN** the provider reports that the challenge expired before the user scanned it
- **THEN** the command requests a new challenge and shows it again, up to a fixed retry limit, then fails with an actionable message

#### Scenario: Unscanned challenge times out

- **WHEN** the timeout elapses without a successful scan
- **THEN** the command stops polling, reports a timeout, and leaves the configuration unchanged

#### Scenario: User cancels

- **WHEN** the user interrupts the command with Ctrl+C
- **THEN** the command stops polling and exits without writing a credential

### Requirement: Acquired credentials are verified before they are stored

A login result MUST be verified with an authenticated provider request before it is persisted. The
command MUST derive the provider account identity from the verified result when the provider makes it
available and MUST refuse to persist a credential that fails verification. The account used for reuse
and naming MUST be derived from the stored params when the provider can derive it offline; if the
provider's reported account contradicts that derivation, the command MUST fail before persistence.

#### Scenario: Verified cookie is accepted

- **WHEN** the provider returns a cookie that authenticates successfully
- **THEN** the command proceeds to store the credential and records the account identity

#### Scenario: Verification failure stores nothing

- **WHEN** the acquired credential fails verification
- **THEN** the command reports the failure and does not create or modify any credential

#### Scenario: Inconsistent provider account is rejected

- **WHEN** the account reported by the provider differs from the account derivable from the returned credential params
- **THEN** the command fails before writing anything and reports the inconsistency

### Requirement: Login stores exactly one named credential per account

The login result MUST be persisted as one `[[credentials]]` entry with the selected provider type. The
credential name MUST come from `--name` when supplied. When `--name` is omitted, the command MUST reuse
an existing credential of the same type that resolves to the same account, and otherwise create one
with a derived name. If `--name` names an existing credential that belongs to a different account, or
reuse would cross accounts, the command MUST fail instead of overwriting or merging. If `--name` names
an existing credential whose account cannot be resolved, the command MAY update it as a deliberate
override, but MUST warn that the existing account could not be verified and MUST NOT describe the
result as a confirmed same-account reuse. Existing configuration entries that the login does not
target MUST be preserved.

#### Scenario: First login creates a credential

- **WHEN** no credential of the selected type exists and the login succeeds
- **THEN** one credential entry is added with the provider type and the verified secret

#### Scenario: Second login for the same account reuses the credential

- **WHEN** a credential of the same type already resolves to the authenticated account
- **THEN** its secret is updated and no duplicate credential is created

#### Scenario: Name collision across accounts is refused

- **WHEN** `--name` identifies an existing credential whose account differs from the authenticated account
- **THEN** the command fails without changing that credential and names the conflicting account

#### Scenario: Explicit name overrides a credential with an unknown account

- **WHEN** `--name` identifies an existing credential whose account cannot be resolved but the authenticated account is known
- **THEN** the command updates that credential, prints a warning that the existing account could not be verified, and does not claim the accounts matched

#### Scenario: Unknown account is not silently reused

- **WHEN** `--name` is omitted and an existing credential's account cannot be resolved
- **THEN** the command does not treat it as the same account and creates a separately named credential instead

#### Scenario: Unrelated configuration survives login

- **WHEN** the login result is written into an existing configuration file
- **THEN** unrelated mounts, credentials, and global settings are unchanged

### Requirement: Configuration changes are transactional

The command MUST compute the credential update, the optional mount, all wizard answers, account
resolution, and conflict checks on an in-memory candidate configuration. It MUST validate the complete
candidate and write the configuration file exactly once, only after every step has succeeded.
Cancellation MUST be honored at every checkpoint before the atomic rename; the rename is the commit
point. If any step fails, is cancelled before that point, or times out before that point, the on-disk
configuration MUST remain byte-for-byte unchanged and MUST NOT contain a credential, a mount, or a
partially written file. A cancellation observed after the final pre-rename check MUST NOT roll back a
committed write and MUST NOT leave a partial file.

#### Scenario: Wizard failure discards the login result

- **WHEN** the login succeeds but the user answers the mount questions with values that fail validation
- **THEN** the command reports the validation error and the configuration file is unchanged, with no credential added

#### Scenario: One write for both entries

- **WHEN** login both creates a credential and adds a mount to an existing configuration
- **THEN** the file is written once and no intermediate version containing only the credential or only the mount is observable
#### Scenario: Rename is the commit point

- **WHEN** cancellation is observed only after the final pre-rename check
- **THEN** the atomic rename completes, the new configuration is treated as committed, and no partial file remains

#### Scenario: Interrupted write leaves the old file

- **WHEN** the final atomic write fails
- **THEN** the previous configuration remains readable and unchanged, and no partial file is left behind

### Requirement: Mount setup has explicit interactive and non-interactive rules

The command SHALL treat mount setup as follows:

- On an interactive terminal without `--no-input`, it MUST ask for the mount name, the provider root
  path, and the mount point, prefilled with defaults, and then add the mount.
- Otherwise, when a configuration file already exists and no mount flag is supplied, it MUST save only
  the credential and print a runnable `qrypt mount add` command for the user to run next.
- Otherwise, when any of `--mount-name`, `--root-path`, or `--mount-point` is supplied, it MUST create
  the mount without prompting. Missing values MUST use the same defaults as the wizard.
- When no configuration file exists, interactive mode bootstraps the file with a mount; non-interactive
  mode MUST require at least one mount flag and MUST fail before starting the provider login when none
  is given, because a credential-only configuration does not satisfy the configuration contract.

Defaults are: mount name derived from the credential name while avoiding existing and reserved mount
names, root path `/`, and the configured mount point or `~/Qrypt` when none is configured. A
user-supplied mount name that already exists MUST fail instead of replacing the existing mount.

#### Scenario: Wizard creates a mount

- **WHEN** the user accepts the defaults of the post-login wizard
- **THEN** a mount referencing the credential is added to the configuration and the command prints `qrypt mount` as the next step

#### Scenario: Non-interactive run saves only the credential in an existing config

- **WHEN** a configuration exists, stdin is not a terminal or `--no-input` is given, and no mount flag is supplied
- **THEN** only the credential is saved and the command prints a runnable `qrypt mount add` command that would create the mount

#### Scenario: Any mount flag switches to non-interactive mount creation

- **WHEN** only `--root-path` is supplied without `--mount-name` or `--mount-point`
- **THEN** the command creates the mount with the derived name, the supplied root path, and the configured or default mount point, without prompting

#### Scenario: Missing configuration without mount flags fails before login

- **WHEN** no configuration file exists, non-interactive mode is active, and no mount flag is supplied
- **THEN** the command exits before starting the provider flow with an error that names the mount flags or the interactive alternative, and no credential is obtained or written

#### Scenario: Missing configuration with mount flags is bootstrapped

- **WHEN** no configuration file exists and at least one mount flag is supplied
- **THEN** the command creates `~/.qrypt/qrypt.toml` with the credential and the mount in one write

#### Scenario: Explicit mount name is not overwritten

- **WHEN** the requested mount name already exists in the configuration
- **THEN** the command fails and reports the conflict instead of replacing the existing mount

#### Scenario: Invalid wizard input is rejected before writing

- **WHEN** the entered mount name or root path would produce an invalid configuration
- **THEN** the command reports the validation problem and does not write the configuration

### Requirement: Configuration file selection and creation are explicit

An explicit `--config PATH` MUST refer to an existing file; when it does not, the command MUST fail
before starting the provider flow, name the path, and MUST NOT create the file or its parent
directory. Without `--config`, an existing discovered configuration MUST be used as-is. When no
configuration is discovered and mount setup requires one, the command MUST create
`~/.qrypt/qrypt.toml`, creating its parent directory with owner-only permissions and the file with
mode 0600.

#### Scenario: Explicit missing config fails before login

- **WHEN** a user runs `qrypt credential login --type quark --config /missing/qrypt.toml`
- **THEN** the command fails naming `/missing/qrypt.toml` before showing a QR code and creates no directory or file

#### Scenario: Discovered config is reused

- **WHEN** `./qrypt.toml` exists and `--config` is omitted
- **THEN** the command updates that file and prints its path

#### Scenario: Home config is created with owner-only permissions

- **WHEN** no configuration is discovered and the command bootstraps one
- **THEN** `~/.qrypt/qrypt.toml` is created with the parent directory mode 0700 and the file mode 0600

### Requirement: A stored login credential is immediately usable and observable

The stored credential MUST be usable by a mount that references it without an additional login step.
After login it MUST appear in `qrypt credential list` and `qrypt credential show` with its account
identity even when no runtime session exists yet.

Account identity and display metadata MUST be resolved independently. The account MUST be derived from
the credential's stored params when the provider can decode them, falling back to session metadata
only when the params cannot identify the account. Display metadata MUST come from an existing session
whenever one exists, even when the account was derived from the params: the source is `state`, and
version and update time reflect the session. With no session, a derivable account is reported with
source `config`; version and update metadata MUST NOT be invented. A credential whose driver does not
support login MUST still be resolved from its session, or reported as unknown, without an error.

#### Scenario: New credential serves a mount

- **WHEN** the user creates a mount that references the credential produced by login and mounts it
- **THEN** the mount authenticates from that credential and does not prompt for a cookie

#### Scenario: Credential-only login is observable

- **WHEN** the user runs `qrypt credential list` or `qrypt credential show <name>` after a login that created no mount
- **THEN** the account is derived from the stored params, the source is reported as `config`, session version and update fields are absent or `-`, and the secret is masked

#### Scenario: Session metadata appears after first use

- **WHEN** a mount has opened the credential and committed a session
- **THEN** the listing and show output report the session source and version for that credential

#### Scenario: Static account and session metadata coexist

- **WHEN** the account is derivable from the stored params and a session also exists
- **THEN** the account comes from the params while the source, version, and update time come from the session

#### Scenario: Driver without login support remains observable

- **WHEN** a credential's driver does not implement login and a session exists
- **THEN** list and show report the session account and metadata without an error, and report unknown fields as `-`

### Requirement: Secrets are not exposed

The command and its logs MUST NOT print the credential secret. The token-bearing challenge URL MUST
appear only in the explicit challenge display and MUST NOT appear in errors, progress messages,
diagnostics, or logs.

#### Scenario: Success output contains no secret

- **WHEN** login succeeds and reports the credential name and configuration path
- **THEN** neither the cookie value nor the token-bearing URL appears in that output

#### Scenario: Failure output contains no secret

- **WHEN** verification, timeout, cancellation, or a wizard step fails
- **THEN** the error text contains neither the cookie value nor the token-bearing URL

### Requirement: Login failures leave the configuration unchanged and are actionable

A provider error, verification failure, timeout, cancellation, or wizard validation failure MUST NOT
create, modify, or delete any credential or mount. Every failure MUST report a concrete next step.

#### Scenario: Provider error is actionable

- **WHEN** the provider reports a login failure
- **THEN** the command reports the failure with a command the user can run to retry, and the configuration is unchanged
