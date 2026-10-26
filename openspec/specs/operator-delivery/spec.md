# operator-delivery Specification

## Purpose

Get the operator from source onto the dev cluster reproducibly and prove, with real traffic, that editing the spec file changes what the gateway serves.

## Requirements

### Requirement: Image is built for the cluster's architecture
The operator image SHALL be built for `linux/amd64`, whatever the build host's architecture, and published to the project's Artifact Registry repository under an immutable tag.

#### Scenario: Build on Apple Silicon
- **WHEN** the image is built and pushed from an arm64 macOS host
- **THEN** the published manifest includes `linux/amd64` and the Pod runs on GKE nodes

### Requirement: Deployment references an immutable image
The operator Deployment SHALL reference its image by immutable tag or digest. That repository SHALL be on the image allowlist.

#### Scenario: Image reference inspected
- **WHEN** the deployed operator Pod spec is read
- **THEN** its image is in the allowlisted Artifact Registry repository and does not use `latest`

### Requirement: Installation follows dependency order
The installation SHALL apply resources so that each step's dependencies already exist: the Envoy runtime, then the namespace, Gateway, and fixtures, then the operator (CRD, RBAC, Deployment), then the input ConfigMap, and the OpenAPIRoute last. Removing the operator CRD SHALL be a separate, explicit uninstall step.

#### Scenario: Fresh install
- **WHEN** the documented install runs on a cluster with the Envoy runtime
- **THEN** every step succeeds without retrying for missing kinds or CRDs

### Requirement: CI checks every operator change
CI SHALL run formatting, vet, build, unit, generated-code/CRD consistency, and envtest checks on changes to the operator source. CI SHALL NOT require cloud credentials.

#### Scenario: Generated CRD is stale
- **WHEN** an API type changes without regenerating the CRD manifests
- **THEN** CI fails

### Requirement: GKE acceptance proves spec edits change traffic
An opt-in acceptance command SHALL run against an explicitly named kube context. It SHALL change the spec, republish it, and assert both route status and real HTTP responses through the Gateway, using bounded waits. A skipped or blocked live check SHALL be reported as not passed.

#### Scenario: Method routing
- **WHEN** GET and POST `/flights` are sent
- **THEN** GET is answered by flight-query and POST by flight-command

#### Scenario: Templated routes
- **WHEN** GET and PATCH `/flights/{id}` and GET `/flights/{id}/status` are sent
- **THEN** each is answered by its bound backend

#### Scenario: Operation added
- **WHEN** `DELETE /flights/{flightId}` is added to the file, bound to flight-command, and republished
- **THEN** DELETE requests reach flight-command without an operator restart

#### Scenario: Binding changed
- **WHEN** an operation's binding is moved to the other fixture and republished
- **THEN** its traffic moves to that fixture

#### Scenario: Operation removed
- **WHEN** an operation is removed from the file and republished
- **THEN** its former requests receive a Gateway 404

#### Scenario: Invalid edit
- **WHEN** an invalid or unsupported edit is published
- **THEN** status shows the error and previously valid requests still succeed

#### Scenario: Non-routing edit
- **WHEN** only descriptions change
- **THEN** the HTTPRoute generation is unchanged

#### Scenario: Literal beats template
- **WHEN** `GET /flights/search` is bound to flight-command beside `GET /flights/{flightId}`
- **THEN** `/flights/search` reaches flight-command and `/flights/abc` reaches flight-query

#### Scenario: Negative traffic
- **WHEN** requests use a wrong Host, an undeclared method, `/flights/`, `/flights//status`, `/flights/x/status/extra`, or `/flights-extra`
- **THEN** each receives a Gateway 404 without reaching a fixture

#### Scenario: Drift, restart, and ownership
- **WHEN** the owned route is edited or deleted, the operator is restarted, and an unrelated HTTPRoute exists
- **THEN** the owned route converges and the unrelated route is unchanged

#### Scenario: Cleanup
- **WHEN** the OpenAPIRoute is deleted
- **THEN** its HTTPRoute is garbage-collected and no catch-all route remains

### Requirement: Failures leave evidence
On any acceptance failure, the suite SHALL capture OpenAPIRoute status, route manifests and conditions, backend identities, and operator logs.

#### Scenario: Assertion fails
- **WHEN** an acceptance assertion fails
- **THEN** the captured diagnostics are written to the run's output directory
