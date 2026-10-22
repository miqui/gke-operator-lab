# Spec Delta

## Purpose

Run an isolated, non-public Envoy Gateway on the dev cluster as the routing target for operator-generated HTTPRoutes, with Gateway API definitions owned by the lab.

## ADDED Requirements

### Requirement: Runtime versions are pinned and compatible
The installation SHALL pin an Envoy Gateway release that supports the cluster's Kubernetes version. It SHALL install the Gateway API CRD bundle that matches that release. If no compatible combination exists, it SHALL stop rather than upgrade the cluster.

#### Scenario: Compatible versions are installed
- **WHEN** the runtime is installed
- **THEN** the installed Envoy Gateway and Gateway API versions match the documented pins
- **AND** the cluster's Kubernetes version is within that release's supported range

### Requirement: Lab owns the Gateway API CRDs
The runtime installation SHALL be the sole installer of Gateway API CRDs on the cluster. Before installing, it SHALL confirm that no provider-managed Gateway API CRDs exist, and it SHALL stop if any do.

#### Scenario: Provider-managed CRDs are present
- **WHEN** Gateway API CRDs carrying provider-managed ownership are found before installation
- **THEN** installation stops and reports them without overwriting them

### Requirement: Exercise traffic is non-public
The exercise SHALL use a dedicated GatewayClass `flights-envoy` and a Gateway in `flights-lab` whose proxy Service is ClusterIP from creation. No Ingress, LoadBalancer Service, DNS record, or certificate SHALL be created.

#### Scenario: Gateway is created
- **WHEN** the Gateway is reconciled
- **THEN** its generated proxy Service has type ClusterIP and no LoadBalancer Service ever existed for it

### Requirement: Local access is loopback only
Access SHALL be through a port-forward bound to `127.0.0.1:8083`, with requests addressed to the listener hostname `flights.example.test`.

#### Scenario: Request through the port-forward
- **WHEN** a request is sent to `127.0.0.1:8083` with `Host: flights.example.test`
- **THEN** it reaches the Envoy proxy
- **AND** the port-forward is not reachable on non-loopback host interfaces

### Requirement: Routes attach only from the exercise namespace
The Gateway listener SHALL accept HTTPRoutes only from `flights-lab`.

#### Scenario: Route from another namespace
- **WHEN** an HTTPRoute in another namespace references the exercise Gateway
- **THEN** the Gateway does not accept it

### Requirement: Readiness is judged by route acceptance
Runtime verification SHALL check controller availability, Gateway readiness, and HTTPRoute Accepted and ResolvedRefs conditions for the current generation. Rejected or stale conditions SHALL count as failures, not Pod readiness alone.

#### Scenario: Backend reference is unresolved
- **WHEN** an HTTPRoute's ResolvedRefs condition is False
- **THEN** verification fails and names the route and condition

### Requirement: Removal preserves shared infrastructure
Exercise removal SHALL delete only exercise-owned resources, in dependency order. It SHALL retain the cluster, platform applications, and the Gateway API CRDs. Removing the CRDs SHALL be a separate, explicit action.

#### Scenario: Exercise is removed
- **WHEN** the documented removal procedure runs
- **THEN** the Gateway, GatewayClass, and Envoy Gateway release are removed
- **AND** the cluster and Gateway API CRDs remain
