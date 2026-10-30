# API compatibility and deprecation policy

StePanel's machine-readable API contract is [`openapi.yaml`](openapi.yaml).
The dashboard and supported SDKs target routes under `/api/v1`. The product
version in the OpenAPI document identifies the StePanel release; the path
prefix identifies the API major version.

## Before StePanel 1.0

The `/api/v1` namespace is present, but the API is still in beta while
StePanel's production gates remain open. Request and response behavior may
change between pre-1.0 releases. Changes to documented operations, schemas,
authentication, or job states must be recorded in `CHANGELOG.md`; operator
migration instructions must be added when an existing integration needs to
change. Generated clients and shell completions must be regenerated from the
OpenAPI contract and checked with `make api-check`.

Unversioned browser routes are compatibility aliases for the UI. They are not
part of the automation compatibility promise and must not be used as the basis
for generated clients.

## After StePanel 1.0

Within an API major version, the following changes are backward-compatible:

- Adding an endpoint or an optional request field.
- Adding a response field.
- Adding a documented response or job-state enum value. Clients must tolerate
  unknown values and must not assume the published enum is exhaustive.

The following changes are breaking and require a new API major version:

- Removing or renaming an endpoint, request field, or response field.
- Changing a field's type, meaning, requiredness, or units.
- Changing authorization requirements, destructive-operation confirmation,
  idempotency behavior, or durable-job terminal semantics.
- Removing or changing the meaning of a documented status code or error
  envelope in a way that requires existing clients to behave differently.

Security fixes may require an exceptional incompatible change when no safe
compatible mitigation exists. The security advisory and release notes must
explain the affected versions, required client or operator action, and any
credential rotation or recovery steps.

## Deprecation and support

After 1.0, a deprecated operation remains available for at least two stable
minor releases and 90 days after its deprecation notice, whichever is longer.
The notice must appear in the OpenAPI operation (`deprecated: true`), the
release notes, and the API migration guidance. The release that removes it
must use a new API major version.

When a new API major version reaches general availability, the previous API
major remains supported for at least 12 months, subject to the product's
published security support window. The supported StePanel release and API
major combinations must be listed in the release notes. Beta releases do not
receive these post-1.0 deprecation or overlap guarantees.

## Release checks

Every API change must update the OpenAPI contract and regenerate supported
clients and completions. Before release, run:

```sh
make api-check
make release-check
go test ./... -run 'TestOpenAPI'
```

The documentation workflow checks links, generated API artifacts, route
coverage, and the API contract tests. A release candidate must also be tested
with its generated SDKs and the previous supported API major where one exists.
