# share-links Specification

## Purpose

Let a user hand out a link to content that already lives in a mounted cloud drive, and manage those
links afterwards — without naming a provider, a driver, a socket path, or a provider share id.

## Requirements

### Requirement: Share link commands are provider-neutral and owned by a running Core

The system MUST expose share-link management through a provider-neutral command surface. The mount
that owns the content MUST be the only selector of the provider; no command may require a provider
name, driver name, socket path, or provider share identifier as input. Share-link commands MUST
operate against a running Core and MUST fail with a clear runtime error when none is running,
without creating or mutating any provider state.

#### Scenario: Provider is selected by the mount-qualified path

- **WHEN** a user runs `qrypt share create /quark-download/report.pdf`
- **THEN** qrypt uses the `quark-download` mount's provider without any provider-specific flag

#### Scenario: No running Core

- **WHEN** a user runs a share-link command while no Core is running for the selected configuration
- **THEN** the command fails with a runtime error naming the missing Core and creates no link

#### Scenario: Provider identifiers stay in diagnostic detail

- **WHEN** a user shows or removes a link
- **THEN** the only identities the user must supply are the link or the qrypt-visible share id, and any provider-specific identifier remains internal

### Requirement: Link creation is synchronous and names exactly one mount

Creating a link MUST be a synchronous request that returns the link, its expiry, and its passcode
visibility in the same response. The request MUST name existing content through mount-qualified
paths; paths from different mounts MUST be rejected in one invocation. A provider that answers
asynchronously MUST be polled inside the driver under a bounded window and MUST surface a typed
timeout instead of blocking indefinitely.

#### Scenario: Link is returned immediately

- **WHEN** a user creates a link for one or more entries on one mount
- **THEN** the command prints the share URL and the effective expiry before returning

#### Scenario: Cross-mount selection is rejected

- **WHEN** a user passes paths from two different mounts in one create call
- **THEN** qrypt rejects the request and creates no link, because a link belongs to one provider

#### Scenario: Missing content is rejected before the provider call

- **WHEN** a requested path does not exist or is not mount-qualified
- **THEN** qrypt returns a usage error and creates no provider share

#### Scenario: Encrypted mounts can publish links

- **WHEN** a user creates a link for content inside a mount that applies qrypt encryption
- **THEN** qrypt publishes the link without rejecting the mount; only receive destinations are restricted

#### Scenario: Queued provider work times out instead of hanging

- **WHEN** the provider does not finish the create task inside the bounded window
- **THEN** qrypt reports a typed timeout error and leaves `share list` as the recovery path

### Requirement: Links expose provider-neutral policy options

Link creation MUST accept an expiry over the neutral set (`1d`, `7d`, `30d`, `forever`), an optional
passcode (explicit or locally generated), an optional visit limit, an optional name, optional
download and edit permissions, and an optional public/private mode. It MUST also honor a provider's
maximum number of entries per link. Options a provider cannot honor MUST be rejected before the
provider call rather than silently ignored.

#### Scenario: Passcode link

- **WHEN** a user creates a link with `--passcode`
- **THEN** qrypt generates or accepts a passcode, the link requires it to open, and the passcode is returned to the owner

#### Scenario: Permanent public link

- **WHEN** a user creates a link with `--expires forever` and no passcode
- **THEN** the link opens without a passcode and has no expiry

#### Scenario: Download and edit permissions

- **WHEN** a user creates a link with download or edit permissions enabled or disabled
- **THEN** the provider receives those permission choices and `show` reports the resulting policy

#### Scenario: Unsupported option is rejected

- **WHEN** a user requests a visit limit from a provider that does not support it
- **THEN** qrypt returns a structured error and creates no link

#### Scenario: Multiple entries exceed the provider limit

- **WHEN** a user requests a single link for more entries than the provider supports
- **THEN** qrypt rejects the request before the provider call and reports the supported maximum

### Requirement: The driver declares which link policies it supports

Each driver MUST declare a share policy: whether a passcode is required, optional, or unsupported;
which expiries it accepts; whether title, visit limits, public/private mode, download permission, and
edit permission exist; the maximum number of entries per link; whether updates exist; whether it can
list the user's links; and whether saves are synchronous or pollable. The system MUST validate a
request against that declaration before any provider call, and MUST report an unsupported option
instead of silently dropping it.

#### Scenario: A provider that requires a passcode

- **WHEN** a link is created on a provider whose policy requires a passcode and the request omits one
- **THEN** qrypt rejects the request as a usage error instead of creating a link the provider would set a hidden passcode on

#### Scenario: A provider without passcodes

- **WHEN** a user requests `--passcode` on a provider whose policy does not support passcodes
- **THEN** qrypt rejects the request and explains that this provider's links are always public

#### Scenario: A provider without visit limits

- **WHEN** a user requests `--limit-visits` on a provider that cannot enforce it
- **THEN** qrypt rejects the request rather than ignoring the limit

#### Scenario: A provider without download or edit controls

- **WHEN** a user requests a download or edit permission change that the provider cannot express
- **THEN** qrypt rejects the request before creating or updating the link

#### Scenario: A provider that cannot list links

- **WHEN** a user runs `share list` on a provider whose policy has no listing support
- **THEN** qrypt reports the capability gap with a stable error and performs no provider call

#### Scenario: Expiry set differs from the neutral set

- **WHEN** a user requests an expiry the acting provider does not accept
- **THEN** qrypt rejects it before the provider call and lists the accepted values

### Requirement: Links can be listed, shown, updated, and removed

The system MUST list the acting mount's links with their title, URL, expiry, passcode visibility,
entry count, size, and visit counters; MUST show one link by link id or share id; MUST update name,
expiry, passcode, and public/passcode mode; and MUST remove one or more links. Removal MUST accept
the user-facing link id and MUST resolve it to the provider's management id internally.

#### Scenario: Listing shows state, not provider jargon

- **WHEN** a user lists links
- **THEN** each row reports title, URL, public/passcode mode, expiry, entry count, size, and visits without provider-specific field names

#### Scenario: Ambiguous mount is reported

- **WHEN** a list, show, update, or remove is run while several mounts are configured and no `--mount` is given
- **THEN** qrypt lists the candidate mounts and asks for an explicit choice instead of guessing

#### Scenario: Unknown link is a not-found error

- **WHEN** a user shows or removes a link that does not exist
- **THEN** qrypt returns a not-found error with a stable code and changes nothing

#### Scenario: Update applies the requested policy

- **WHEN** a user updates a link's expiry, passcode, or name
- **THEN** subsequent `show` reports the new policy and the link's content is unchanged

### Requirement: Link secrets are redacted by default

Command output MUST redact passcodes unless the caller explicitly asks for secrets. Logs MUST NEVER
contain a passcode or a provider session token.

#### Scenario: Redacted show

- **WHEN** a user shows a passcode-protected link
- **THEN** the passcode is printed as `****` and `--show-secrets` is offered

#### Scenario: Owner reads the passcode deliberately

- **WHEN** a user runs `share show` with `--show-secrets` on their own link
- **THEN** the stored passcode is printed so the user can forward it

#### Scenario: Redirected output does not leak

- **WHEN** a share or receive command writes JSON to a file or pipe
- **THEN** passcodes and session tokens are still redacted unless secrets were explicitly requested

### Requirement: Unsupported providers fail clearly

When the acting mount's driver does not declare the share capability, the system MUST fail before
any provider call with an actionable error that names the mount and the missing capability.

#### Scenario: Driver without link support

- **WHEN** a user creates a link on a mount whose driver cannot create links
- **THEN** qrypt reports `share_unsupported` and creates nothing
