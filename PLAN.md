# Plan: Build and Test an OpenAPI 3.2 HTTPRoute Operator

## 1. Outcome and scope

Deliver a working Kubernetes operator that reads an OpenAPI 3.2 specification file, reconciles Gateway API HTTPRoutes, and demonstrates the resulting traffic changes through Envoy Gateway on the GKE dev environment.

**Envoy installation and hand-written routes alone are not completion.** Completion requires editing the flights specification, publishing that change to Kubernetes, observing operator-generated route changes, and verifying actual HTTP traffic.

This is an implementation plan, not an implementation report. No cloud resources are changed by this document.

### Confirmed direction

- Branch: `feat/envoy-flights-gateway`.
- GCP display name: `dev1`; project ID: `dev1-486920`; project number: `186319603637`.
- Region: `us-central1`.
- Create the GKE dev cluster for this work with the repository's `gke-deploy.sh` (`dev-cluster`, zonal `us-central1-a`); use Envoy Gateway for this exercise's traffic.
- Use the repository's dev-cluster scripts, not a parallel infrastructure solution.
- Keep required API enablement in `gke-deploy.sh`.
- Create the cluster with GKE's managed Gateway API disabled; this lab owns the Gateway API CRDs, pinned to the selected Envoy Gateway release.
- Publish the operator image to Artifact Registry in `dev1-486920`, built for `linux/amd64`.
- Use flights, not Petstore.

### Proposed MVP boundaries

- Go, Kubebuilder/controller-runtime, and a pinned OpenAPI library verified against 3.2 fixtures.
- One watched namespace, `flights-lab`, containing the operator, input ConfigMap, custom resource, backend Services, and generated routes.
- Envoy's controller in `envoy-gateway-system`, with a dedicated GatewayClass and Gateway.
- ClusterIP proxy Service and localhost-only port-forward access; no public load balancer, DNS, or TLS initially.
- One generated HTTPRoute per custom resource, with explicit capacity checks rather than automatic route sharding.
- Bundled YAML/JSON OpenAPI input with internal references; no remote document fetching or external references in the MVP.
- Kubernetes Service backends in the same namespace; no cross-namespace ReferenceGrants initially.

These are design defaults for implementation, not claims about features already present.

## 2. Repository review and its implications

The review covered the project files across all root scripts and documentation, `.github/`, `k8s/`, and `openspec/`, including chart values, application wiring, policies, and fixtures. `src/` contains no application source files. Ignored secret values in `.env`, Git internals, and installed workflow tooling are not application inputs to this plan.

| Existing surface | Observed behavior | Plan implication |
| --- | --- | --- |
| `gke-deploy.sh:29-40,127-133,214-240,282-288` | Requires PROJECT_ID, enables APIs, provisions the dev foundation, skips cluster creation when its describe succeeds, and fetches kubeconfig | Reuse this setup path; do not treat API enablement or a missing local context as missing project design |
| `gke-deploy.sh:31-33` | Defaults to dev-cluster in us-central1-a | Create the cluster with these repository defaults; region us-central1 does not mean a regional cluster |
| `gke-deploy.sh:216-240` | Cluster create passes no `--gateway-api` flag | Add `--gateway-api=disabled` explicitly so GKE never installs or reverts its own Gateway API CRDs |
| `gke-deploy.sh:154-157` | Node SA has only `roles/container.defaultNodeServiceAccount`; comment assumes every image is public | Add `artifactregistry.googleapis.com` and a repository-scoped `roles/artifactregistry.reader` grant for the operator image |
| `gke-bootstrap.sh:23-58` | Selects the expected context and rejects manifests for another project | Retarget project-bound manifests before platform bootstrap |
| Scripts, docs, `k8s/platform/crossplane-*.yaml`, `k8s/external-secrets/values.yaml` | Retargeted from dev-ai-1 to dev1-486920 on `main` (441ff5d) | Done; `gke-bootstrap.sh`'s project check passes for dev1-486920 |
| `k8s/argocd/root-application.yaml:21-22` and all child Applications | Read miqui/gke-ai-observer on main | Local feature-branch files are not automatically deployed; verify source ownership before touching GitOps |
| `gke-secrets-seed.sh:22`, `gke-bootstrap.sh:33`, `.env.example` | No secrets are configured | Flight stubs and the operator do not need a new secret store or 1Password dependency |
| `check-policies.sh:27`, `polaris-audit.sh:21` | Workload inventories are empty | Add actual first-party operator and flight manifests to both inventories |
| `k8s/policies/overlays/`, `audit-only/disallow-latest-tag.yaml` | Deny covers default; workload audits cover headlamp/polaris | New namespaces are not protected merely by adding an image allowlist entry |
| `.github/workflows/policy-check.yml` | Runs policy and Polaris checks for selected infrastructure paths | Add operator build/unit/integration CI; existing CI does not test Go or reconciliation |
| `k8s/headlamp/`, `k8s/polaris/`, `gke-port-forward.sh` | Read-only tools, namespace quotas, NetworkPolicies, loopback access | Preserve these patterns, but allow necessary Envoy/backend/controller traffic explicitly |
| `k8s/crossplane/`, `k8s/platform/`, `k8s/trivy-operator/` | Existing platform dependencies and continuous reports, but no managed experiment resources | Keep them intact; the HTTPRoute operator does not require new GCP permissions or Crossplane resources |
| `HELM.md`, `ARGOCD.md`, `k8s/argocd/argocd-values.yaml` | Pinned charts, app-of-apps health ordering, self-healing and pruning | Respect a single owner per resource; generated routes must not be committed as Argo-owned desired state |
| `src/`, project root | No operator source, go.mod, Dockerfile, CRD, or functional operator tests | Scaffold the operator and its complete build/test/deploy path |
| `.gitignore`, `.gitattributes` | Secret/editor exclusions, no Go-specific artifact exclusions yet | Add only generated build/test exclusions needed by the new module |

At review time, the branch has no commits or configured remote and every file is untracked. Commit the current baseline before scaffolding so operator work lands as a reviewable diff (`.env` is already gitignored). Tool executables exist for gcloud, kubectl, Helm, Kyverno, Go via an asdf shim, and Docker. Kubebuilder, setup-envtest, and Polaris are missing. Executable presence does not prove a configured Go version or running Docker daemon.

### Cluster creation

An earlier GKE listing returned an API-disabled-or-never-used error, and no kubectl context was configured. The decision is to create the dev cluster for this work: `PROJECT_ID=dev1-486920 ./gke-deploy.sh`, after the script changes in §7A. The script is idempotent and skips resources whose describe succeeds, so if a `dev-cluster` turns out to exist already, it is reused rather than replaced. Local operator development (compiler, unit tests, envtest) does not wait on the cluster.

## 3. End-to-end architecture and ownership

```text
examples/flights/openapi.yaml + OpenAPIRoute deployment bindings
                         |
                publish stable ConfigMap
                         |
                         v
                 Operator watches inputs
                         |
             parse --> validate --> compile plan
                         |
                         v
              owned gateway.networking.k8s.io/v1 HTTPRoute
                         |
                         v
                    Envoy Gateway
                         |
                         v
             Envoy proxy (ClusterIP Service)
                         |
               +---------+---------+
               |                   |
          flight-query        flight-command

Test client --> 127.0.0.1:8083 port-forward --> Envoy proxy
```

The specification is a normal version-controlled file. A deployment/test command publishes its contents into a stable-name ConfigMap; the operator watches Kubernetes events, not a developer's local filesystem. Applying a changed file must update that ConfigMap without restarting the operator or manually editing HTTPRoutes.

Use a stable ConfigMap generator name, or equivalent explicit publication, so content updates do not require changing the custom resource reference. Do not bake the source document into the operator image.

| Resource | Owner |
| --- | --- |
| OpenAPI source file and deployment bindings | Developer/Git; Argo CD when a source is explicitly configured |
| Input ConfigMap and OpenAPIRoute custom resource | Deployment workflow, not the operator |
| Generated HTTPRoute spec and ownership metadata | This operator |
| HTTPRoute parent status, Gateway status, generated proxy resources | Envoy Gateway |
| Shared Gateway API CRDs | Existing provider/installation owner |
| Existing platform applications and GCP foundation | Existing repository workflows |

Never have Argo CD and this operator both reconcile the same generated HTTPRoute. Hand-written routes may be temporary diagnostic fixtures only and must be removed before operator acceptance tests.

### APIOps workflow: adding a route

The spec file is the only thing a developer edits; nobody writes or patches an HTTPRoute. Example: add `DELETE /flights/{flightId}` (cancel a flight), served by flight-command.

1. **Edit the spec.** Add the operation to `examples/flights/openapi.yaml` with a required `operationId`:
   ```yaml
   /flights/{flightId}:
     delete:
       operationId: cancelFlight
       parameters: [{ $ref: '#/components/parameters/FlightId' }]
       responses: { '204': { description: Cancelled } }
   ```
2. **Bind a backend, only if it is not the default.** Unbound operations go to `defaultBackendRef` (flight-query). A write goes to flight-command, so add one entry to the OpenAPIRoute:
   ```yaml
   operationBindings:
     - operationId: cancelFlight
       backendRef: { name: flight-command, port: 8080 }
   ```
3. **Publish.** `./flights-publish.sh` (create-configmap dry-run piped to `kubectl apply`; kustomize can't load a file outside `k8s/flights/`). The ConfigMap name stays `flights-openapi`, so it gets new data and the OpenAPIRoute reference doesn't change. The binding change in step 2 is applied with `kubectl apply -k k8s/flights`. Later, with GitOps, the publish step is a merged PR that Argo CD syncs.
4. **Operator reconciles.** The ConfigMap change triggers a reconcile through the field index. The operator parses, validates, and compiles the whole plan; `sourceDigest` and `planDigest` both change, so it patches the owned HTTPRoute with a new match `{method: DELETE, path: RegularExpression ^/flights/[^/]+$}` in the flight-command rule. If the edit were description-only, only `sourceDigest` would change and the route would not be written.
5. **Envoy Gateway programs the proxy.** The HTTPRoute reports Accepted/ResolvedRefs; the operator rolls that into its Ready condition.
6. **Verify.**
   ```sh
   kubectl -n flights-lab get openapiroute flights -o jsonpath='{.status}'
   curl -i -X DELETE -H 'Host: flights.example.test' http://127.0.0.1:8083/flights/<id>   # served by flight-command
   ```

If the edit is invalid (bad YAML, missing operationId, unbound backend, unsupported method like QUERY), the operator sets an error condition, applies nothing from the new plan, and the existing routes keep serving. Removing an operation works the same way: delete it from the file, publish, and its requests start getting a Gateway 404.

## 4. Flights contract and backend fixtures

A Flight represents one scheduled flight instance on a particular date; flightNumber is not its unique identity.

| Field | Type | Required |
| --- | --- | --- |
| id | UUID, assigned by the service | yes |
| flightNumber | string, e.g. AC102 | yes |
| origin / destination | three-letter uppercase IATA airport codes | yes |
| scheduledDeparture / scheduledArrival | RFC 3339 date-times with timezone | yes |
| estimatedDeparture / estimatedArrival | RFC 3339 date-times with timezone | no |
| status | scheduled, boarding, departed, arrived, cancelled, diverted | yes |

Use separate Flight, CreateFlight, UpdateFlight, and FlightStatus schemas. Creation excludes id; updates are partial and require at least one update field. Examples must have coherent arrival/departure timestamps.

| Method and path | operationId | Backend |
| --- | --- | --- |
| GET /flights | listFlights | flight-query |
| POST /flights | createFlight | flight-command |
| GET /flights/{flightId} | getFlight | flight-query |
| PATCH /flights/{flightId} | updateFlight | flight-command |
| GET /flights/{flightId}/status | getFlightStatus | flight-query |

The fixtures return distinguishable backend identities and representative JSON. They need not implement persistence, bookings, airline scheduling, authentication, or full flight business logic. Define responses so POST and PATCH are actually supported; do not assume a static web server accepts writes. Fixtures answer any method and path with their identity, so add-operation tests (e.g. a new DELETE) exercise routing without fixture changes.

Optional list filters such as origin, destination, and date are application parameters, not mandatory Gateway match predicates.

## 5. Operator input contract and routing semantics

Introduce a namespaced `OpenAPIRoute` CRD (proposed `routing.lab.dev/v1alpha1`) with a status subresource and a small deployment contract:

| Input | MVP meaning |
| --- | --- |
| documentRef.name / key | Same-namespace ConfigMap and its OpenAPI YAML/JSON entry |
| parentRefs | Existing dedicated Gateway/listener |
| hostnames | Explicit deployment hostnames, initially flights.example.test |
| basePath | Explicit deployment path prefix, initially empty |
| defaultBackendRef | Same-namespace Service and port, initially flight-query |
| operationBindings | Backend overrides keyed by operationId, initially createFlight/updateFlight to flight-command |

Validate required fields, Service ports, binding targets, and duplicate operationIds. OpenAPI operationId is optional in the standard; the supported MVP profile requires it for each routable operation and reports missing values explicitly rather than inventing Service names.

Deployment hostnames/basePath are authoritative for this projection. Document that OpenAPI servers describe API locations but are not Kubernetes backend bindings. Do not automatically create exposure from every server URL; reject operation/path-level server overrides that cannot be represented by the declared projection.

### Supported translation

- Accept explicitly declared OpenAPI 3.2.0 YAML/JSON and validate document structure and routing-relevant references, not just the version string.
- Handle ordinary inbound operations under paths, including internally referenced path items and required path parameters.
- Use Exact matches for literal paths.
- Support parameters occupying a whole path segment by escaping literal text and using anchored single-segment regexes, e.g. `^/flights/[^/]+$`.
- Include an explicit HTTP method on every generated match. Never omit a method as an approximation for an unsupported one.
- Preserve literal-path precedence; reject overlapping template patterns whose precedence cannot be represented reliably. Gateway API leaves RegularExpression precedence implementation-specific; this design relies on Envoy Gateway ranking Exact above RegularExpression above PathPrefix. Anchored single-segment templates do not overlap each other, so only literal-vs-template collisions (e.g. `/flights/search` beside `/flights/{flightId}`) depend on that ordering, and the e2e suite covers it.
- Sort and normalize the plan deterministically and group compatible matches where possible.
- Respect the pinned CRD's rule, match, and object-size limits. Reject oversized plans before changing the existing route; sharding is out of scope initially.
- Recognize OpenAPI webhooks/callbacks as non-ingress operations and report that they are not projected into these routes.

### Explicitly unsupported in the MVP

- QUERY or custom additionalOperations methods not representable by the installed HTTPRoute method schema/controller.
- Partial-segment templates, ambiguous template overlaps, unresolved/external references, server-variable expansion, and unsupported routing overrides.
- Inferring authentication, request/response validation, rewrites, CORS, rate limits, or backend Services from OpenAPI declarations.

A valid OpenAPI 3.2 document can be unsupported for routing. Report "invalid document" separately from "unsupported routing feature"; never silently drop an operation.

Gateway path matching is not UUID/schema validation. A non-empty non-UUID segment can reach a backend, which remains responsible for parameter validation.

## 6. Reconciliation and failure behavior

1. Watch OpenAPIRoute changes, the specifically referenced ConfigMap, and owned HTTPRoutes. Watch relevant Service/Gateway changes needed to recompute dependency/readiness status. Map ConfigMap events back to OpenAPIRoutes through a field index on `spec.documentRef.name`, not a namespace-wide list scan.
2. Fetch and validate all inputs and compile the complete desired plan before route writes.
3. Create or patch a deterministically named route with an ownerReference to the custom resource. Refuse to adopt or overwrite an unrelated object at that name.
4. Change managed fields only; leave Envoy-written status and unrelated metadata alone. Avoid writes when the desired route spec has not changed.
5. Reconcile route removals and backend changes from the new plan. Removing every operation must remove the owned route, not accidentally create a default prefix rule matching all traffic.
6. Repair manual route-spec drift and recreate a deleted route when the source remains valid.
7. On malformed/unsupported input, missing document, or invalid bindings, preserve existing owned routes and expose the error. If no valid route exists, create none.
8. Retry transient API conflicts/errors with controller-runtime's standard mechanisms; do not report success when writes fail. Restarting the operator must recover from Kubernetes state, not an in-memory-only cache.
9. Delete generated routes when their owning custom resource is deleted using Kubernetes garbage collection. Do not add a finalizer unless implementation identifies an actual non-GC resource to clean up.

Preserving existing routes on invalid input is not a promise to reconstruct a historical plan if somebody also deletes those routes while the source is invalid. Do not fabricate a route from invalid input.

### Status and observability

- Status shape: `observedGeneration`, `sourceDigest`, `planDigest`, the generated route name, and conditions.
- Conditions distinguish source validity/support, route application, dependency resolution, and readiness.
- Record the observed custom-resource generation and source content digest; ConfigMap edits do not increment the custom resource's generation.
- Track source digest separately from normalized routing-plan digest so schema/description-only edits can be acknowledged without route churn.
- Read HTTPRoute Accepted and ResolvedRefs for the relevant parent/current route generation, plus the relevant Gateway's readiness. Do not require a non-portable HTTPRoute Programmed condition.
- Conditions do not prove backend reachability; real HTTP checks do.
- Emit useful Events and structured logs without dumping the entire input document or credentials. Expose health/readiness probes and normal controller metrics; no new observability stack is necessary.
- Keep runtime RBAC namespaced and minimal. Separate CRD/role installation permissions from the controller ServiceAccount; the operator needs no GCP IAM role or Secret read permissions.

## 7. Implementation workstreams

### A. Dev-environment setup, reusing this repository

1. Verify project identity (`dev1-486920`) and the configured dev-cluster defaults.
2. Extend the explicit API list in gke-deploy.sh:
   - container.googleapis.com and compute.googleapis.com for GKE/Compute infrastructure;
   - secretmanager.googleapis.com, iam.googleapis.com, and iamcredentials.googleapis.com for existing platform integrations;
   - artifactregistry.googleapis.com for the operator image (new).
3. Add `--gateway-api=disabled` to the cluster create command so GKE never manages Gateway API CRDs on this cluster (confirm the flag against the installed gcloud; it is creation-relevant, not a later toggle to rely on).
4. Add an idempotent Artifact Registry Docker repository (proposed `operator-lab` in `us-central1`) and grant `gke-dev-nodes` `roles/artifactregistry.reader` on that repository only, not project-wide. Update the node-SA comment that says every image is public.
5. Done on `main` (441ff5d): existence probes go through an `exists` helper that returns absent only on NotFound and aborts on permission, disabled-API, or network errors.
6. Create the cluster with `PROJECT_ID=dev1-486920 ./gke-deploy.sh`. Treat private-control-plane authorized-IP failures using the documented GKE access procedure (the script prints the re-authorize command).
7. If full platform bootstrap is required, reconcile the repository URL/revision consistently (the project-bound manifests are already retargeted). Merely setting GITHUB_REPO does not rewrite child Application URLs.
8. Full Argo CD/Crossplane bootstrap is not a prerequisite for the operator: phase 1 needs only the cluster, Envoy Gateway, the fixtures, and the operator. Do not run gke-teardown.sh during exercise setup or cleanup.

No broad IAM grant is implicit; the only new grant is the repository-scoped reader above.

### B. Operator module and local development

1. Create a self-contained Kubebuilder Go module under proposed `src/openapi-route-operator/`; there is no existing application layout to preserve.
2. Pin Go/Kubebuilder/controller-runtime/Gateway API client versions compatibly. Evaluate libopenapi using actual 3.2 fixtures before committing to its parser/validation APIs.
3. Scaffold CRD, scheme registration, controller, status, namespaced RBAC, probes, generated manifests, Make targets, and a multi-stage container build.
4. Put pure parsing/translation in an independently testable package; keep Kubernetes reads/writes and status aggregation in the reconciler.
5. Establish dependency/tool installation only when the selected commands require it. Verify Go's shim resolves a version and Docker is responsive before relying on either.
6. Support a fast local manager run against the explicitly selected dev-cluster context with only one active manager for the watched scope. Enforce that with controller-runtime leader election (a Lease in `flights-lab`), enabled for both `make run` and the deployed manager, so the two can never write routes concurrently.

### C. Envoy runtime and flight fixtures

1. Pin a supported Envoy Gateway release compatible with the cluster's Kubernetes version. Do not treat the previously researched candidate as a verified cluster fit.
2. The lab owns the Gateway API CRDs: install the bundle matching the pinned Envoy Gateway release (via its chart, or separately with `--skip-crds`), and confirm before install that no GKE-managed CRDs are present. Use the same Gateway API version for envtest. Never upgrade the cluster as a workaround.
3. Configure ClusterIP (EnvoyProxy `envoyService.type: ClusterIP`, referenced from the GatewayClass `parametersRef`) before Gateway creation; establish flights-envoy GatewayClass, the dedicated Gateway, listener hostname, and same-namespace allowedRoutes.
4. Add distinguishable flight stubs and Services plus the source OpenAPI file, stable ConfigMap publication, and sample OpenAPIRoute. Do not ship production hand-written routes alongside generated ones.
5. Verify Envoy's regex and path normalization behavior, especially duplicate slashes, encoded slashes, and trailing slashes; encode the intended boundary in tests, not assumptions.

### D. Deployable operator image and platform integration

1. Add a documented operator image build/publish path, not just a local binary: `docker buildx build --platform linux/amd64` pushed to `us-central1-docker.pkg.dev/dev1-486920/operator-lab/openapi-route-operator`, authenticated with `gcloud auth configure-docker us-central1-docker.pkg.dev`.
2. Build for the GKE node architecture. A macOS/ARM development host must not accidentally publish an ARM-only image for amd64 nodes.
3. Deploy by immutable tag or digest. Nodes pull through the repository-scoped reader grant from §7A; no image pull Secret is introduced.
4. Add a k8s/operator installation entry point using the generated CRD/RBAC/manager manifests, plus separate k8s/envoy-gateway and k8s/flights entries. Deploy dependencies before custom resources.
5. Add the Artifact Registry repository to `restrict-image-repositories.yaml` with the other new image repositories.

#### Phase 2 hardening (after phase 1 acceptance passes)

Kyverno currently enforces only in `default`, and Dataplane V2 does not restrict namespaces without NetworkPolicies, so `flights-lab` and `envoy-gateway-system` work without the items below. They follow phase 1 acceptance instead of gating it.

6. Follow existing GitOps patterns when a published source is confirmed. During branch-local iteration, isolate experiment resources outside existing self-healed applications and keep a single owner; do not repoint the whole platform root.
7. Extend the enforcement namespace selector to cover first-party flights/operator workloads, and extend audit selectors appropriately for third-party Envoy workloads. Update the tag-audit selector too. Keep all existing namespaces covered.
8. Add exact image repositories, CPU/memory requests and limits, non-root/drop-ALL/no-escalation/seccomp settings, probes, and bounded namespace resources. Verify generated Envoy Pods as well as checked-in workloads.
9. Add necessary NetworkPolicy flows for API access, DNS, Envoy control/data-plane communication, backend traffic, and probes. Do not copy a dashboard's deny-all-ingress policy unchanged onto the backends.
10. Update both WORKLOAD_DIRS inventories and prove Kyverno actually matches the added namespaces; keep its nonzero-rule/error checks and known-bad self-test. Keep Polaris report-only and Trivy reports available.

## 8. Verification strategy

Each implementation workstream lands its focused tests and documentation with the behavior, rather than postponing all verification until GKE.

### Unit tests: parser and compiler, no cluster required

- All five flights operations produce the intended method/path/backend plan.
- Malformed YAML/JSON, invalid 3.2 structures, missing parameters/operationIds, duplicate IDs, bad bindings, and external references fail explicitly.
- QUERY/custom methods, ambiguous templates, unsupported servers, and capacity overflow are rejected without broadening matches.
- Literal regex characters are escaped; nested paths and whole-segment templates have the expected boundaries.
- Identical inputs and reordered nonsemantic data produce a stable plan.
- Description/schema-only changes leave route spec/hash unchanged; operation removals change the plan.
- An empty routable API yields an explicit empty desired resource set, never an HTTPRoute with implicit catch-all defaults.

### Controller integration tests: envtest

- Install pinned Gateway API and operator CRDs into the test API server; test real status subresources, updates, and resource-version conflicts.
- Create a custom resource and document, update only ConfigMap data, and observe the owned route change.
- Exercise invalid-input preservation, source deletion, missing backends, foreign-object collisions, drift repair, and route recreation under valid input.
- Verify schema-only changes cause no HTTPRoute spec generation bump.
- Exercise restart recovery, source digest tracking, status errors, and retry behavior.
- Verify ownerReferences and cleanup intent; envtest has no garbage collector or Envoy controller, so real GC and data-plane behavior require the next layer.

### GKE end-to-end tests: operator plus Envoy, required for completion

| Scenario | Required evidence |
| --- | --- |
| Initial reconcile | Apply only input document/bindings; operator creates routes accepted by Envoy |
| Method routing | GET /flights identifies flight-query; POST /flights identifies flight-command |
| Templated routes | GET/PATCH instance and GET status reach intended backends |
| Add operation | Add an operation to the file, republish ConfigMap, observe route and traffic appear without operator restart |
| Change binding | Point an operation at an alternate valid fixture, observe traffic move |
| Remove operation | Remove it from the file and verify its former request receives Gateway 404 |
| Invalid/unsupported edit | Error condition appears; previously valid traffic remains; no partial plan is applied |
| Non-routing edit | Source is acknowledged, route spec/generation does not churn |
| Drift and restart | Edit/delete an owned route under valid input, restart manager, and verify convergence |
| Ownership | An unrelated HTTPRoute remains unchanged; a name collision reports an error |
| Empty API / CR deletion | No accidental catch-all; generated routes disappear and unrelated resources remain |
| Literal vs template precedence | Add a literal path beside a template at the same depth (e.g. `GET /flights/search` with `GET /flights/{flightId}`); the literal request reaches its bound backend, other values reach the template's |
| Negative traffic | Wrong host/method/path shape does not reach a fixture backend |
| Readiness failure | Broken Service/Gateway reference yields explicit non-ready status, not a false success |

All traffic checks go through the port-forward with `Host: flights.example.test` (or `curl --resolve`), since the listener matches that hostname. Use bounded waits for observed state, not fixed sleeps as proof. Capture status, route manifests, backend identities, and diagnostic logs on failure. Live checks must not skip and then report overall success.

### CI and reproducible commands

- Add Go formatting/vet, unit, build, generated-code/CRD consistency, and envtest checks for operator source and dependency changes.
- Preserve the existing pinned Kyverno/Polaris workflow and update its path selection where integration files move or expand.
- Pin required test tooling; obtain compatible envtest binaries through the module's test target.
- Provide a documented GKE acceptance command with explicit context and failure reporting. Live cloud deployment is opt-in, not an automatic pull-request job with cloud credentials.
- If network/credentials block GKE checks, report local results separately and leave end-to-end completion pending.

## 9. Delivery order and documentation

Phase 1 (operator end to end):

1. Commit the current baseline on `feat/envoy-flights-gateway`.
2. Apply the §7A gke-deploy.sh changes (Artifact Registry API, repository and reader grant, `--gateway-api=disabled`; probes and project targeting are already on `main`) and create the cluster. Local work below does not wait on it.
3. Scaffold the Kubebuilder module; implement the input contract and pure compiler with rejection/precedence/limit tests.
4. Implement reconciliation, ownership, status, leader election, and envtest cases.
5. Install the pinned Envoy Gateway with lab-owned Gateway API CRDs, the ClusterIP EnvoyProxy, and the flights fixtures.
6. Build the amd64 image, push it to Artifact Registry, deploy the operator, then run the full file-change-to-traffic acceptance suite.
7. Document build, setup, publication, reconcile status, failure recovery, and exercise-only cleanup using commands that were actually exercised.

Phase 2: the §7D hardening items (GitOps ownership, policy selectors, workload security settings, NetworkPolicies, audit inventories), re-running the acceptance suite after NetworkPolicies land.

Update README.md with the actual operator architecture and status; HELM.md with selected Envoy/CRD versions; GKE-OPS.md/KUBECTL.md with project-correct setup and troubleshooting; and policy/Argo documentation where ownership or namespace coverage changes. Do not rewrite unrelated historical notes.

## 10. Completion and removal

The project is complete only when the operator is built, deployed, and shown to create/update/remove HTTPRoutes from file changes, with passing focused tests and real Envoy traffic evidence on GKE.

On exercise removal, delete the sample custom resource first and verify route garbage collection, then remove fixture resources and the dedicated operator/runtime in dependency order. Retain the existing cluster, platform applications, unrelated routes, and shared/provider-managed CRDs. Deleting the operator CRD would delete all its custom resources, so make that a separate explicit uninstall action.

## 11. Relationship to existing OpenSpec artifacts

The existing [add-envoy-flights-gateway proposal](openspec/changes/add-envoy-flights-gateway/proposal.md), [design](openspec/changes/add-envoy-flights-gateway/design.md), [runtime requirements](openspec/changes/add-envoy-flights-gateway/specs/envoy-gateway-runtime/spec.md), [flight requirements](openspec/changes/add-envoy-flights-gateway/specs/flights-routing-fixture/spec.md), and [tasks](openspec/changes/add-envoy-flights-gateway/tasks.md) describe only the earlier Envoy baseline and explicitly defer the operator.

They are not the complete implementation contract for this revised goal. Before implementing through OpenSpec, create a separate change (proposed `add-openapi-route-operator`) covering the operator, setup corrections, image delivery, and acceptance suite above, rather than stretching the existing change; do not claim the old 15-task checklist delivers the operator.
