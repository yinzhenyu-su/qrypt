## ADDED Requirements

### Requirement: Parameters declare their configuration scope

The canonical schema SHALL declare a scope for every built-in driver parameter, one of `credential`,
`mount`, or `both`. Generated runtime metadata SHALL expose that scope, validation SHALL enforce it
when a parameter appears on a credential or on a mount, and generated documentation SHALL present it.
A built-in parameter without a declared scope MUST fail the schema check; parameters contributed by
dynamically registered drivers MAY default to `both`.

#### Scenario: Scope reaches runtime metadata

- **WHEN** generated driver metadata is inspected
- **THEN** each built-in parameter reports the same scope declared in the schema

#### Scenario: Validation enforces scope

- **WHEN** a mount declares a credential-scope parameter, or a credential declares a mount-scope parameter
- **THEN** validation rejects the offending definition with an error naming the parameter and its scope

#### Scenario: Shared parameter is allowed in both places

- **WHEN** a driver declares a `both` parameter
- **THEN** validation accepts it on either a credential or a mount, and a mount value takes precedence

#### Scenario: Missing built-in scope fails CI

- **WHEN** a built-in driver parameter omits its scope
- **THEN** the schema check rejects the change
