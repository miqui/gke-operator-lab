# Proposal

## Why

API routes on the Envoy gateway should follow the OpenAPI 3.2 document that describes them: editing the spec file and publishing it should be the only step needed to add, re-bind, or remove a route. Nothing translates a spec into Gateway API routes today, and the lab has no cluster, gateway runtime, or operator to do it. This change delivers phase 1 of PLAN.md: a working operator, proven end to end with real traffic on a new GKE dev cluster.

## What Changes

- Extend `gke-deploy.sh` for this project: enable Artifact Registry, create a repository that the nodes can pull from (repository-scoped grant only), and create the cluster with GKE's managed Gateway API disabled. Then create the cluster in `dev1-486920`. Project retargeting and NotFound-safe probes are already on `main`.
- Install a pinned Envoy Gateway with lab-owned Gateway API CRDs, a dedicated GatewayClass and Gateway, and a ClusterIP proxy that is reached only through a loopback port-forward.
- Add an OpenAPI 3.2 flights contract and two fixture backends (flight-query, flight-command) that report their identity for any method and path. Publish the spec to a stable-name ConfigMap.
- Add a Kubebuilder operator with an `OpenAPIRoute` CRD (`routing.lab.dev/v1alpha1`). It compiles the referenced spec into one owned HTTPRoute, reports status, and preserves working routes when an edit is invalid.
- Build the operator for `linux/amd64`, publish it to Artifact Registry, and deploy it with leader election.
- Add unit tests, envtest tests, and an opt-in GKE acceptance suite that covers file-change-to-traffic behavior, plus Go CI checks.
- Supersede `add-envoy-flights-gateway`. Its decisions to reuse an existing cluster, reuse provider-managed CRDs, and ship hand-written routes no longer apply. That change should be dropped, not archived or synced.

Out of scope:
- **PLAN.md phase 2:** Kyverno/Polaris coverage of the new namespaces, workload hardening, NetworkPolicies, and GitOps ownership of the experiment.
- **Not planned:** public exposure, TLS, route sharding, external `$ref`s, and inferring auth, validation, or rewrites from the spec.

## Capabilities

### New Capabilities

- `dev-cluster-provisioning`: How `gke-deploy.sh` provisions the dev cluster foundation, including its Gateway API mode and operator image registry access.
- `envoy-gateway-runtime`: An isolated, non-public Envoy Gateway runtime with lab-owned Gateway API CRDs and readiness checks based on route acceptance.
- `flights-routing-fixture`: The flights OpenAPI 3.2 contract, the identity-reporting fixture backends, and spec publication to a stable ConfigMap.
- `openapi-route-compilation`: Translation of an OpenAPI 3.2 document plus deployment bindings into a deterministic HTTPRoute plan, including what is rejected as invalid or unsupported.
- `openapi-route-reconciliation`: The `OpenAPIRoute` API and its controller: watches, owned-route writes, drift repair, failure preservation, status, and a single-writer guarantee.
- `operator-delivery`: Building, publishing, and installing the operator, and the GKE acceptance suite that proves spec edits change live traffic.

### Modified Capabilities

None. `openspec/specs/` is empty. `envoy-gateway-runtime` and `flights-routing-fixture` reuse the names from the unsynced `add-envoy-flights-gateway` change, with their requirements rewritten for this change.

## Impact

- **Scripts:** `gke-deploy.sh` (API list, Artifact Registry repository and grant, cluster create flag).
- **New code:** `src/openapi-route-operator/` (Go module, CRD, controller, compiler, Dockerfile, Makefile, tests).
- **New manifests:** `k8s/envoy-gateway/`, `k8s/flights/`, `k8s/operator/`, and `examples/flights/openapi.yaml`.
- **Policy:** the Artifact Registry repository is added to `restrict-image-repositories.yaml`. That is the only policy change in phase 1.
- **CI:** a new Go build, unit, and envtest workflow. The existing policy workflow is unchanged.
- **Docs:** README.md, HELM.md, GKE-OPS.md, and KUBECTL.md, updated with commands that were actually run.
- **Cloud:** a new zonal GKE cluster, the VPC/NAT foundation, and an Artifact Registry repository in `dev1-486920`. The only new IAM grant is a repository-scoped `roles/artifactregistry.reader` for `gke-dev-nodes`.
- **Dependencies:**
  - Envoy Gateway: candidate v1.9.2 with Gateway API v1.6.1, to be confirmed against the cluster's Kubernetes version.
  - controller-runtime and Kubebuilder.
  - An OpenAPI library verified against 3.2 fixtures; the candidate is libopenapi.
- **Branch:** all work goes on `feat/envoy-flights-gateway`. `main` stays the generic lab baseline.
