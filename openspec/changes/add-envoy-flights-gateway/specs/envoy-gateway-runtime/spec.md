# Spec Delta

## Purpose

Provide an isolated Envoy Gateway runtime for routing experiments on the existing GKE cluster without changing the platform or exposing public endpoints.

## ADDED Requirements

### Requirement: Existing cluster identity is verified before deployment
Deployment SHALL require an explicitly selected existing cluster in project `dev1-486920`, project number `186319603637`, in region `us-central1`. Failed identity or access checks SHALL stop deployment without creating a cluster, enabling APIs, or changing the active global gcloud project.

#### Scenario: Target cannot be verified
- **WHEN** the GKE API is disabled, access is denied, or no matching cluster is identified
- **THEN** deployment stops with the failing prerequisite and does not modify cloud resources

#### Scenario: Existing target is verified
- **WHEN** the configured context and live cluster metadata match the designated existing cluster
- **THEN** the experiment can proceed without recreating or upgrading that cluster

### Requirement: Gateway API ownership is preserved
Installation SHALL verify the pinned Envoy release's compatibility with the cluster Kubernetes version and installed Gateway API CRDs. It SHALL preserve provider-managed and pre-existing shared CRDs and stop if their compatibility cannot be established.

#### Scenario: Compatible provider-managed CRDs exist
- **WHEN** the existing Gateway API CRDs meet the selected release's requirements
- **THEN** the experiment reuses them without transferring ownership or reinstalling them

#### Scenario: Existing CRDs are incompatible
- **WHEN** the installed Gateway API CRDs are incompatible with the selected Envoy release
- **THEN** deployment reports the incompatibility without overwriting CRDs or upgrading the cluster

### Requirement: Exercise traffic is isolated and non-public
The experiment SHALL use a dedicated Envoy GatewayClass, Gateway, namespaces, and ClusterIP proxy Service. It SHALL expose local access only through port-forwarding bound to `127.0.0.1`, without creating an Ingress, public LoadBalancer Service, DNS record, or certificate.

#### Scenario: Exercise is deployed
- **WHEN** Envoy reconciles the exercise Gateway
- **THEN** its proxy Service has type ClusterIP and exercise routes reference only the dedicated Gateway

#### Scenario: Local access is opened
- **WHEN** the documented port-forward procedure starts
- **THEN** the listener is bound to loopback rather than all host interfaces

### Requirement: Runtime readiness includes route acceptance
The verification procedure SHALL inspect controller availability, Gateway readiness, and HTTPRoute acceptance and resolved references for the relevant parent and observed generation. It SHALL report rejected or stale conditions as failures rather than relying solely on Pod readiness.

#### Scenario: A backend reference is unresolved
- **WHEN** an exercise HTTPRoute has a failing ResolvedRefs condition
- **THEN** verification fails and identifies the route and condition

### Requirement: Removal preserves shared infrastructure
Removal SHALL delete only explicitly identified exercise-owned resources. It SHALL retain the existing cluster, platform applications, provider-managed CRDs, pre-existing shared CRDs, and unrelated Gateway resources.

#### Scenario: Experiment is removed
- **WHEN** the documented removal procedure is executed
- **THEN** exercise workloads and its dedicated runtime are removed without deleting the cluster or shared Gateway API definitions
