# openapi-route-reconciliation Specification

## Purpose

Define the `OpenAPIRoute` API and how the operator keeps one owned HTTPRoute in line with the referenced spec: when it writes, what it refuses to touch, how it fails safely, and what status it reports.

## Requirements

### Requirement: OpenAPIRoute declares the deployment contract
A namespaced `OpenAPIRoute` resource SHALL declare the following fields: `documentRef` (a same-namespace ConfigMap name and key), `parentRefs`, `hostnames`, an optional `basePath`, `defaultBackendRef`, and `operationBindings`. Invalid field values SHALL be rejected by the API schema where possible.

#### Scenario: Required fields missing
- **WHEN** an OpenAPIRoute is created without `documentRef` or `defaultBackendRef`
- **THEN** the API server rejects it

### Requirement: Spec content changes are reconciled
The operator SHALL reconcile when the OpenAPIRoute changes, when its referenced ConfigMap changes, or when its owned HTTPRoute changes. A ConfigMap update SHALL be reflected in the route without restarting the operator.

#### Scenario: Operation added to the published spec
- **WHEN** an operation is added to the spec and the ConfigMap is republished
- **THEN** the owned HTTPRoute gains the matching rule without an operator restart

### Requirement: Routes are compiled in full before any write
The operator SHALL read all inputs and compile the complete plan before it writes the route. It SHALL never apply part of a plan.

#### Scenario: One operation in an edit is unsupported
- **WHEN** an edit adds one valid operation and one QUERY operation
- **THEN** neither is applied and the previous route is unchanged

### Requirement: The operator writes only routes it owns
The operator SHALL manage one deterministically named HTTPRoute per OpenAPIRoute, owned via an ownerReference. If an object it does not own already exists at that name, it SHALL report a conflict and leave that object untouched.

#### Scenario: Name collision
- **WHEN** an unrelated HTTPRoute already exists at the generated name
- **THEN** the operator reports a conflict condition and does not modify it

#### Scenario: Unrelated routes are left alone
- **WHEN** other HTTPRoutes exist in the namespace
- **THEN** they are never modified or deleted by the operator

### Requirement: Writes happen only when the route spec changes
The operator SHALL update only the fields it manages. It SHALL NOT write when the desired route spec matches the live one, and it SHALL leave status written by Envoy Gateway untouched.

#### Scenario: Description-only edit
- **WHEN** only descriptions change in the published spec
- **THEN** the status reports the new source digest and the HTTPRoute's generation does not change

### Requirement: Invalid input preserves working routes
When the document is missing, invalid, unsupported, or bound incorrectly, the operator SHALL keep the existing owned route unchanged and report the error. If no owned route exists yet, it SHALL create none.

#### Scenario: Invalid edit after a valid one
- **WHEN** a valid spec is replaced with one that fails compilation
- **THEN** the previous route keeps serving traffic and status reports the failure

### Requirement: Drift is repaired
While the source is valid, the operator SHALL restore manual edits to the owned route spec and SHALL recreate the owned route if it is deleted.

#### Scenario: Route deleted by hand
- **WHEN** the owned HTTPRoute is deleted while the spec is valid
- **THEN** the operator recreates it

### Requirement: An empty plan removes the route
When the compiled plan is empty, the operator SHALL delete the owned HTTPRoute rather than leave a route with no or default matches.

#### Scenario: Every operation removed
- **WHEN** the published spec has no routable operations
- **THEN** the owned HTTPRoute is deleted

### Requirement: Deleting the OpenAPIRoute removes its route
Deleting an OpenAPIRoute SHALL remove its owned HTTPRoute through Kubernetes garbage collection. Unrelated resources SHALL remain.

#### Scenario: Custom resource deleted
- **WHEN** an OpenAPIRoute is deleted
- **THEN** its HTTPRoute is garbage-collected and the ConfigMap, Services, and Gateway remain

### Requirement: Status reports source, plan, and readiness separately
Status SHALL report the following fields: `observedGeneration`, a source digest, a plan digest, the generated route name, and conditions. The conditions SHALL distinguish source validity from route application, dependency resolution, and readiness. Ready SHALL require the route to be Accepted and ResolvedRefs for the current generation and the parent Gateway to be ready.

#### Scenario: Missing backend Service
- **WHEN** a binding references a Service that does not exist
- **THEN** status reports a dependency failure and Ready is False

#### Scenario: Healthy route
- **WHEN** the route is Accepted and ResolvedRefs for its current generation and the Gateway is ready
- **THEN** Ready is True and the digests match the published spec

### Requirement: Only one manager writes at a time
Only one operator instance SHALL reconcile at a time, enforced by leader election. This SHALL also hold when a local development run and the in-cluster deployment run together.

#### Scenario: Local run alongside deployment
- **WHEN** a local manager starts while the deployed manager holds the lease
- **THEN** the local manager does not reconcile until it acquires the lease

### Requirement: Restarts recover from cluster state
After a restart, the operator SHALL reach the correct state from Kubernetes objects alone, with no dependence on previous in-memory state.

#### Scenario: Restart after drift
- **WHEN** the owned route is edited by hand and the operator is restarted
- **THEN** the route converges back to the compiled plan

### Requirement: Operator permissions are namespaced and minimal
The operator's runtime identity SHALL be limited to reading OpenAPIRoutes, ConfigMaps, Services, and Gateways, and to managing HTTPRoutes, leases, and events, in its watched namespace. It SHALL have no Secret access and no Google Cloud IAM role.

#### Scenario: Secret access attempt
- **WHEN** the operator's ServiceAccount is checked for permission to read Secrets
- **THEN** access is denied
