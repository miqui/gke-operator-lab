# Design

## Context

See proposal.md for motivation and the six capability specs for required behavior. PLAN.md is the background document. This design records the technical choices it leaves open.

Current state:
- The repository has no Go code. `main` holds the generic lab, including `gke-deploy.sh`, which already has NotFound-safe probes and `dev1-486920` targeting. The project-specific script changes (Artifact Registry and the Gateway API mode) land on `feat/envoy-flights-gateway` only.
- No cluster exists yet. `gke-deploy.sh` creates a zonal Standard cluster on the stable channel, with private nodes, Dataplane V2, and a control plane restricted to the operator's IP.
- Kyverno enforces only in `default`, and no NetworkPolicies exist for the new namespaces. That is why phase 1 can deploy without the phase 2 hardening.
- `restrict-image-repositories` already allowlists `docker.io/library/nginx`.

## Goals / Non-Goals

**Goals:**
- Keep translation as a pure library that is fully unit-testable without a cluster, with the reconciler as a thin layer over it.
- Make every write explainable from two digests: what the source said, and what the route should be.
- Keep the operator's inner development loop local (`make test`, `make run`) and make the GKE stage a single opt-in command.

**Non-Goals:**
- A general-purpose OpenAPI gateway product: multi-namespace watches, cross-namespace backends, ReferenceGrants, or several Gateways per resource.
- Supporting Gateway implementations other than Envoy Gateway in phase 1. Behavior that is implementation-specific (regex precedence, path normalization) is verified against Envoy only.

## Decisions

### 1. Module layout: Kubebuilder go/v4 under `src/openapi-route-operator/`
The module uses the standard Kubebuilder layout:
- `api/v1alpha1`: the `OpenAPIRoute` types.
- `internal/compiler`: a pure package. Input is document bytes plus bindings; output is a `Plan` or typed errors.
- `internal/controller`: the reconciler.
- `test/e2e`: the GKE acceptance suite.

The compiler imports nothing from Kubernetes except the Gateway API types it emits.

*Alternative:* hand-rolled controller-runtime wiring without Kubebuilder. Rejected because Kubebuilder gives CRD generation, RBAC markers, envtest setup, and an e2e skeleton for free, and the CI consistency check depends on its `make manifests generate` targets.

### 2. OpenAPI parsing: libopenapi, gated by 3.2 fixtures, with a defined fallback
The first task is a spike that runs the flights contract plus 3.2-specific fixtures through libopenapi (pb33f). The 3.2 fixtures cover `query` operations, `additionalOperations`, `$self`, and webhooks. The spike checks that libopenapi parses them, resolves internal `$ref`s, exposes every operation including `query`/`additionalOperations` (so they can be rejected explicitly), and refuses or flags external references.

If libopenapi falls short, the fallback is:
1. Validate the document against the official OAS 3.2 JSON Schema with a JSON Schema 2020-12 validator.
2. Walk `paths`, `webhooks`, `components.pathItems`, and callbacks from a generic YAML tree, resolving only `#/...` references.

The routing subset is small enough that this fallback is tractable. kin-openapi is not a candidate, because it targets 3.0.

*Why decide the fallback now:* the specs require invalid-document versus unsupported-feature errors either way. Only the internals differ, so tasks do not change.

**Spike outcome (task 3.2): adopted libopenapi v0.41.6, with two additions.** Using the fixtures in `internal/compiler/testdata/`, libopenapi exposed all five flights operations, `query` (via `PathItem.Query` and in `GetOperations()`), `additionalOperations` (e.g. `PURGE`), `$self`, internal path-item `$ref`s (resolved), webhooks, and callbacks. With the default configuration it never fetched file or URL references. The gaps it left are filled by:
1. **Structural validation:** libopenapi does not validate against the OAS schema (a document missing `info.version` builds). The compiler first validates against the embedded official OAS 3.2 JSON Schema (`https://spec.openapis.org/oas/3.2/schema/2026-08-30`) with `santhosh-tekuri/jsonschema/v6`. This is the same validator task 3.1 used.
2. **External references:** libopenapi reports an external ref as "component … does not exist", which is indistinguishable from a broken internal ref, and it still returns a partial model on error. The compiler pre-scans the YAML tree for any `$ref` not starting with `#` and fails with a dedicated external-reference error before building. It also treats any `BuildV3Model` error as an invalid document, never using the partial model.

libopenapi's `Logger` is set to a discard handler. Its errors are returned, not logged, so the operator's logs never contain document fragments.

### 3. Compiled plan shape and grouping
`Plan` is a sorted list of `{method, pathMatch{type, value}, backendRef, operationId}`. Compilation then works as follows:
- Sorting is by path-match type (Exact before RegularExpression), then by value, then by method. This makes the result deterministic.
- Matches are grouped into HTTPRoute rules, one rule per distinct backendRef, in sorted backend order. The flights baseline is therefore two rules: query and command.
- Literal segments are escaped with `regexp.QuoteMeta` and template segments become `[^/]+`. Patterns are anchored `^…$`.
- The plan digest is the SHA-256 of the plan's canonical JSON. The source digest is the SHA-256 of the raw ConfigMap value.

Precedence relies on Envoy Gateway ordering Exact before RegularExpression. Two templates are ambiguous when some request can match both and neither is strictly more literal at the first segment where they differ. Those are rejected.

*Alternative:* one rule per operation. Rejected because it hits the 16-rule limit at 16 operations, while grouping by backend scales to the per-rule match limit.

### 4. Capacity limits come from the pinned CRD, not constants alone
The compiler takes its limits (rules per route, matches per rule, total matches) as parameters. A unit test loads the vendored Gateway API CRD YAML and asserts that the parameters equal the schema's `maxItems` and CEL limits. A CRD bump that changes them then fails tests instead of drifting silently.

### 5. Writes use server-side apply with ownership pre-check
The reconciler handles each pass in this order:
1. Get the route at the deterministic name, which is the OpenAPIRoute's name.
2. If an object exists there without the OpenAPIRoute's controller ownerReference, set a conflict condition and stop.
3. If the plan is empty, delete the owned route.
4. Otherwise, server-side apply with field manager `openapi-route-operator` and `force: true`.

SSA leaves Envoy Gateway's status and other managers' fields alone. Forcing reclaims the managed fields after manual drift, which satisfies drift repair.

*Implementation note (task 4.2):* there is no separate skip check. Applying an unchanged configuration is a server-side no-op: no new resourceVersion, no generation bump, no watch event. The envtest suite asserts this, since a description-only edit leaves the route's resourceVersion unchanged. A digest-plus-drift check would duplicate what the server already does. The `routing.lab.dev/plan-digest` annotation is still set for humans and the acceptance suite.

*Alternative:* Get, mutate, Update. Rejected because it races Envoy's status writes and needs custom field-merging logic to avoid clobbering unrelated metadata.

### 6. Watches and indexes
The reconciler watches the following:
- `For(OpenAPIRoute)` with a generation-changed predicate, so the operator's own status writes do not retrigger it.
- `Watches(HTTPRoute)`, mapped by name to the same-named OpenAPIRoute, which drives drift repair and recreation. This is used rather than `Owns()` because a foreign route at the generated name must also trigger a reconcile, both to report the conflict and to recover once it is removed.
- `Watches(ConfigMap)`, mapped through a field index on `spec.documentRef.name`.
- `Watches(Service)`, mapped through an index of every backend Service name referenced.
- `Watches(Gateway)`, mapped through an index of `parentRefs`.

All watches and the cache are restricted to `flights-lab`, so namespaced RBAC suffices.

**Removing a bound operation.** A binding that names an operationId missing from the document fails compilation (InvalidBinding), so removing a bound operation from the spec is rejected and the current route keeps serving. The safe order is to remove the operation from the spec first, which is rejected and leaves the route unchanged, and then remove its binding, which applies. Traffic never reaches the wrong backend in between. Removing the binding first would briefly send the operation to the default backend.

### 7. Status conditions
The conditions are `SourceValid`, `RouteApplied`, `DependenciesResolved`, and `Ready`. Reasons distinguish `InvalidDocument`, `UnsupportedRouting`, `InvalidBinding`, `CapacityExceeded`, `NameConflict`, `BackendNotFound`, and `GatewayNotReady`. `Ready` mirrors the route's `Accepted`/`ResolvedRefs` for the route's current `metadata.generation` on the relevant parent, together with the Gateway's `Programmed`.

On failure, `sourceDigest` updates (the new source was observed) but `planDigest` stays at the last applied plan. A mismatch between them is therefore visible.

*Implementation note (tasks 9.1 and 8.3): status reads bypass the cache.* On GKE the informer cache was seen missing status-subresource writes until a restart. That covered Envoy Gateway's writes on the route and sometimes the operator's own writes on the OpenAPIRoute, and it left `Ready=Pending` for routes Accepted for their current generation. The reconciler now reads the OpenAPIRoute at the start of each reconcile, and the route and Gateway in `setReady`, through the manager's uncached `APIReader`. While Ready is `Pending` or `GatewayNotProgrammed` it requeues every 5 s; failures that need a change do not requeue. Verified with a drift-repair stress loop: before the fix, 2–3 of 10 repairs stalled for 30 s or more; after it, 20 of 20 converged within 1 s. Two consecutive acceptance runs then passed 24/24.

### 8. Leader election across local and in-cluster runs
Both `make run` and the Deployment use lease `openapi-route-operator` in `flights-lab`. A developer running locally while the deployment is up waits for the lease rather than fighting it. The documented local loop is to scale the Deployment to zero first.

### 9. Envoy Gateway install: lab-owned CRDs, ClusterIP from the start
Installation proceeds as follows:
1. Install the Gateway API and Envoy CRDs from the `gateway-crds-helm` chart at the pinned version. Install the controller from `oci://docker.io/envoyproxy/gateway-helm` with `--skip-crds`. Both are pinned, with candidate v1.9.2 / Gateway API v1.6.1, confirmed against the live server version before install.
2. Apply an `EnvoyProxy` with `provider.kubernetes.envoyService.type: ClusterIP` and a GatewayClass whose `parametersRef` points at it.
3. Only then create the Gateway.

A preflight script refuses to install if any `gateway.networking.k8s.io` CRD already exists with GKE-managed labels.

A `ClientTrafficPolicy` sets `path.disableMergeSlashes: true` and `escapedSlashesAction: RejectRequest`. Without that, Envoy's default slash merging would turn `/flights//status` into `/flights/status` and route it to getFlight. The e2e negative cases lock this in.

### 10. Fixtures: pinned nginx with an echo config
Both fixtures run `docker.io/library/nginx` (already allowlisted) at a pinned tag. A ConfigMap server block listens on 8080 and serves `return 200` with JSON `{backend, method, path}` for every location. nginx's `return` answers any method, including PATCH and DELETE, so add-operation tests need no fixture changes.

*Alternative:* a small Go echo server. Rejected for phase 1 because it adds a second image to build, publish, and allowlist.

### 11. Spec publication: a script, not kustomize
Kustomize rejects files outside the kustomization root by default, and `kubectl apply -k` cannot relax that. A repo-root `flights-publish.sh` therefore runs `kubectl create configmap flights-openapi --from-file=openapi.yaml=examples/flights/openapi.yaml --dry-run=client -o yaml | kubectl apply -f -` against an explicit context. `k8s/flights/` holds everything else (namespace, fixtures, Gateway, OpenAPIRoute) and is applied with `kubectl apply -k`.

*Alternative:* move the spec under `k8s/flights/`. Rejected to keep the API contract out of the deployment tree, so that GitOps can later publish it the same way.

### 12. Image and registry
The image uses Kubebuilder's multi-stage Dockerfile on `gcr.io/distroless/static:nonroot` and is built with `docker buildx build --platform linux/amd64 --push`. It goes to `us-central1-docker.pkg.dev/dev1-486920/operator-lab/openapi-route-operator:<git-sha>`. `gke-deploy.sh` creates the repository through the existing `exists` helper and grants `gke-dev-nodes` reader on that repository alone.

### 13. Acceptance suite drives copies of the spec
The `test/e2e` Ginkgo suite is invoked with `make test-e2e KUBE_CONTEXT=gke_dev1-486920_us-central1-a_dev-cluster`. It works as follows:
- It publishes variants from `test/e2e/testdata/` (baseline, plus-delete, rebound, minus-operation, invalid, description-only, literal-vs-template) through the same ConfigMap path. It never edits the checked-in example.
- It owns its port-forward on `127.0.0.1` with a random free port, sets `Host: flights.example.test`, and polls with bounded `Eventually` timeouts.
- On failure it writes status, routes, and logs under `test/e2e/artifacts/`, which is gitignored.

### 14. CI
A new workflow `.github/workflows/operator-ci.yml`, filtered to `src/openapi-route-operator/**`, runs these checks:
- `gofmt`/`go vet`.
- `make manifests generate` followed by a clean `git diff` check.
- `make test`, which covers unit tests plus envtest with binaries from `setup-envtest` at the pinned Kubernetes version.
- `make docker-build` without push.

The existing `policy-check.yml` is untouched.

## Risks / Trade-offs

- [libopenapi's 3.2 support is incomplete] → The spike runs first, and the fallback in decision 2 is already chosen.
- [Envoy Gateway's precedence or normalization differs from assumptions] → Decision 9's policy plus e2e negative and literal-vs-template cases fail loudly. Any change in behavior means updating the compiler's rejection rules, not loosening the tests.
- [Regex matching costs more than prefix matching] → Negligible at lab scale. Literal paths stay Exact.
- [One route per OpenAPIRoute caps API size] → A clear capacity error is surfaced. Sharding is a later change.
- [The operator IP changes and the control plane becomes unreachable] → Use the re-authorize command that `gke-deploy.sh` prints. It is documented in GKE-OPS.md.
- [Three e2-standard-2 nodes are tight if the full platform is also bootstrapped] → Phase 1 does not need the platform stack, and autoscaling goes up to 5 nodes.
- [Forced SSA would overwrite a legitimate co-manager] → No other manager is expected on the owned route. Ownership pre-check ensures the operator only forces on its own object.

## Migration Plan

Deployment:
1. Apply the `gke-deploy.sh` changes on the feature branch and run it, which creates the cluster and registry.
2. Preflight the CRDs, then install the CRDs, Envoy Gateway, the EnvoyProxy/GatewayClass/Gateway, and the ClientTrafficPolicy.
3. `kubectl apply -k k8s/flights`, minus the OpenAPIRoute.
4. Build and push the image, then apply `k8s/operator`.
5. Run `flights-publish.sh`, apply the OpenAPIRoute, and run `make test-e2e`.

Rollback and cleanup follow this order:
1. Delete the OpenAPIRoute and confirm the route is garbage-collected.
2. Delete fixtures and the Gateway.
3. Uninstall the operator, then Envoy Gateway.
4. Delete the operator CRD and the Gateway API CRDs only as explicit, separate steps.

The cluster is retained, and `gke-teardown.sh` is not part of this change.

## Open Questions

- The exact Envoy Gateway / Gateway API pin depends on the stable-channel Kubernetes version that GKE assigns at creation. It is confirmed in the install task and recorded in HELM.md.
- The nginx tag and the fixture container port are chosen at implementation time. Only the Service name and port appear in bindings.
