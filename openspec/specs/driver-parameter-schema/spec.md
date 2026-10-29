# driver-parameter-schema Specification

## Purpose

Defines qrypt's declarative driver parameter contract: one schema owns parameter metadata, translations, ordering, and conditional requirements, and both generated runtime metadata and user documentation derive from it.

## Requirements

### Requirement: Schema is the canonical parameter source
The configuration schema SHALL be the single source for built-in driver parameter names, types, defaults, examples, secret markers, ordering, display metadata, and translations. Adding or changing a built-in parameter MUST NOT require editing a second parameter table.

#### Scenario: New parameter is declared once
- **WHEN** a built-in parameter is added to the schema and the generators run
- **THEN** runtime parameter metadata and user documentation both include it
- **AND** no Python metadata table needs editing

#### Scenario: Schema location is stable
- **WHEN** a user or editor references the configuration schema
- **THEN** `schema/qrypt.schema.json` exists and remains valid JSON Schema draft-07

### Requirement: Parameter metadata carries localized descriptions
Every user-visible built-in parameter SHALL declare an English description and a `zh-CN` description via `x-i18n`. Driver definitions SHALL declare a localized title, order, and optional intro.

#### Scenario: Chinese documentation uses schema translations
- **WHEN** documentation is generated
- **THEN** parameter tables and driver sections use `x-i18n.zh-CN` values
- **AND** missing translations fall back to English

#### Scenario: Runtime-only types are preserved
- **WHEN** a parameter's runtime type differs from its JSON Schema type
- **THEN** an explicit schema override keeps the runtime type in generated metadata

### Requirement: Conditional parameter requirements are enforced
Generated runtime metadata SHALL expose both unconditional required parameters and alternative requirement groups. Mount validation SHALL reject a mount unless all unconditional requirements and at least one alternative group are satisfied.

#### Scenario: Required parameters are missing
- **WHEN** a mount omits an unconditional required parameter
- **THEN** validation rejects the mount

#### Scenario: Alternative credentials are satisfied
- **WHEN** a mount satisfies one documented alternative group
- **THEN** validation accepts the credential shape

#### Scenario: Alternative credentials are missing
- **WHEN** a mount satisfies none of the documented alternative groups
- **THEN** validation rejects the mount

### Requirement: Documentation is schema-driven
User-facing parameter documentation SHALL be generated from the schema and SHALL NOT maintain independent driver order, names, descriptions, or translation tables.

#### Scenario: Driver sections follow schema order
- **WHEN** driver documentation is generated
- **THEN** sections appear in schema-declared order
- **AND** names, intros, and descriptions come from schema metadata

### Requirement: Schema drift fails CI
CI SHALL fail when generated Go parameter metadata or generated user documentation does not match the committed schema, or when the schema is missing required metadata.

#### Scenario: Generated Go metadata is stale
- **WHEN** the schema changes without regenerating Go metadata
- **THEN** CI rejects the change

#### Scenario: Generated documentation is stale
- **WHEN** the schema changes without regenerating user docs
- **THEN** CI rejects the change

#### Scenario: Required i18n metadata is missing
- **WHEN** a user-visible parameter lacks `zh-CN` metadata
- **THEN** the schema check rejects the change

### Requirement: Dynamic drivers remain supported
Drivers registered at runtime SHALL remain supported. Such drivers MAY provide their own parameter definitions when no schema-defined built-in definition exists.

#### Scenario: Runtime-registered driver exposes parameters
- **WHEN** a dynamically registered driver declares parameters
- **THEN** `qrypt driver schema` continues to show those parameters

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
