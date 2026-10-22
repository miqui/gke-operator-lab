# Proposal

## Why

The OpenAPI 3.2 operator exercise needs a Gateway implementation that can distinguish flight operations by HTTP method and represent templated paths without broad prefix matches. Envoy Gateway provides a candidate implementation on the existing GKE cluster, without replacing the cluster or relying on GKE's managed Gateway controller for exercise traffic.

## What Changes

- Add a pinned, isolated Envoy Gateway installation and a dedicated GatewayClass, Gateway, and ClusterIP data plane.
- Reuse the existing GKE cluster in project `dev1-486920` (project number `186319603637`), in region `us-central1`; do not create, recreate, or upgrade the cluster.
- Establish Gateway API CRD compatibility and ownership before installation; never overwrite provider-managed CRDs.
- Keep required GCP API enablement explicit in `gke-deploy.sh`; verify its existing GKE, Compute Engine, Secret Manager, IAM, and IAM Credentials API list rather than enabling unrelated APIs from the Envoy installation.
- Add an OpenAPI 3.2 flights contract and distinguishable query/command fixture backends with explicit HTTPRoute resources.
- Demonstrate method-based routing, exact literal paths, anchored templated paths, and unmatched-request behavior through localhost-only port-forwarding.
- Integrate the experiment with existing policy checks and document deployment, verification, and experiment-only removal.
- Keep the OpenAPI-to-HTTPRoute operator itself for a subsequent change; this change establishes its routing target and acceptance fixtures.

## Capabilities

### New Capabilities

- `envoy-gateway-runtime`: Isolated, non-public Envoy Gateway operation on an existing GKE cluster, including compatibility checks and bounded removal.
- `flights-routing-fixture`: An OpenAPI 3.2 flights contract and observable method/path routing fixtures for the future operator.

### Modified Capabilities

None. The project has no existing OpenSpec capability specifications.

## Impact

- Proposed additions: Envoy installation values/manifests, flight fixture manifests and contract, and focused routing checks.
- Existing integration surfaces: `k8s/argocd/`, `k8s/policies/`, `check-policies.sh`, `polaris-audit.sh`, `README.md`, and `HELM.md`.
- Dependencies: a compatible pinned Envoy Gateway release, Gateway API CRDs, and policy-compliant fixture images.
- No changes to cluster provisioning, Google IAM, existing platform applications, or public networking are authorized.
- Deployment is blocked until the existing cluster can be identified: the read-only cluster listing returned a disabled-or-unused GKE API error for the confirmed project, and no current kubectl context is configured.
- The local Git repository has no commits or remote yet; existing Argo CD manifests reference a different repository's `main`. The experiment must not repoint the platform root application.
