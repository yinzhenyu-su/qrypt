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
