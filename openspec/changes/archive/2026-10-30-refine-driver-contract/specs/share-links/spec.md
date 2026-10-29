## MODIFIED Requirements

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
