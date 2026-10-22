# Design

## Context

See proposal.md for motivation and the two capability deltas for acceptance requirements. This change establishes the Envoy routing baseline, not the spec-to-route operator.

Observed repository state:

- Platform applications use Argo CD's app-of-apps pattern, pinned chart versions, and separate chart values and manifests.
- The root and child Applications reference `https://github.com/miqui/gke-ai-observer.git` on `main`, not this new local branch. The local repository currently has no remote or commits.
- The README describes no public entry point. It references `dev-ai-1`, while the confirmed experiment project is `dev1-486920`.
- `check-policies.sh` and `polaris-audit.sh` have workload inventories that must include new first-party fixtures. Image allowlists and namespace policy overlays need explicit consideration.
- `gke-deploy.sh` already enables container.googleapis.com, compute.googleapis.com, secretmanager.googleapis.com, iam.googleapis.com, and iamcredentials.googleapis.com before provisioning. It is a full foundation provisioning script, not a safe API-only command.
- The project identity was verified from project number 186319603637. GKE listing in that project returned a disabled-or-unused API error. No current kubectl context exists. The user confirmed region us-central1, but the cluster name and precise location remain unverified.

These are deployment prerequisites, not permission to create a cluster, enable APIs during planning, change IAM, or repoint the platform's GitOps root.

## Goals / Non-Goals

**Goals:**

- Establish observable method/path routing with Envoy on the retained cluster.
- Keep the control plane, proxy, fixtures, ownership, and removal boundaries explicit.
- Supply a flights contract and route acceptance cases reusable by a future operator.
- Make all live actions conditional on verified cluster identity and compatible dependencies.

**Non-Goals:**

- Build the OpenAPI reconciliation operator in this change.
- Provide production flight business logic, persistence, schema enforcement, authentication, or authorization.
- Create a public load balancer, DNS, TLS configuration, new cluster, or cluster upgrade.
- Replace the existing GKE Gateway controller or its resources.
- Promise lossless translation of arbitrary OpenAPI methods or templates.

## Decisions

### 1. Separate experiment installation from platform reconciliation

Use proposed `k8s/envoy-gateway/` values/manifests and `k8s/flights/` fixture manifests, with a reproducible documented Helm-plus-manifest procedure from the local feature branch. Preserve every existing root Application and platform source.

Do not insert an experiment Application into the current root app-of-apps until a reachable source repository and revision are confirmed. A future GitOps handoff can use experiment-only Applications following the existing multi-source pattern. Never combine Helm ownership and Argo CD ownership of the same release.

Alternative: repoint the root Application to this branch. Rejected because it changes the entire platform and requires a published repository that does not yet exist.

### 2. Pin compatible dependencies and separate CRD ownership

Use `oci://docker.io/envoyproxy/gateway-helm` and its matching CRD chart. The reviewed installation documentation identifies v1.9.2 as a concrete candidate; its compatibility matrix requires Gateway API v1.6.1 and lists Kubernetes v1.33-v1.36 for v1.9. This is a candidate, not a verified fit for the inaccessible cluster.

Before selecting the implementation pin, inspect the actual Kubernetes version and CRD bundle version/channel. If compatible provider-managed CRDs exist, reuse them, install only necessary Envoy-specific CRDs, and disable CRD installation in the main chart. If Gateway API CRDs are absent, install the pinned release's supported standard-channel bundle with explicit ownership; record it as shared infrastructure retained on removal. Verify any chart-managed admission-policy resources separately from CRDs.

Never overwrite provider-managed or incompatible shared CRDs. Stop and report a compatibility block instead of upgrading the cluster or silently choosing an unsupported dependency combination.

### 3. Keep the proxy non-public

Use `envoy-gateway-system` for the control plane and `flights-lab` for the dedicated Gateway and backend fixtures. Use GatewayClass `flights-envoy` with controllerName `gateway.envoyproxy.io/gatewayclass-controller`, and configure EnvoyProxy so the generated proxy Service is ClusterIP.

Restrict the Gateway's allowedRoutes to the exercise namespace. Keep backend references in the same namespace; no ReferenceGrant is needed. Use listener hostname `flights.example.test` and HTTP only for the loopback experiment.

Document discovery of the generated proxy Service by its Gateway labels rather than relying on generated names. Forward its HTTP port to `127.0.0.1:8083` and send the matching Host header. Use the EnvoyProxy ClusterIP setting before creating the Gateway so no transient LoadBalancer Service is created.

Alternative: use the quickstart unchanged. Rejected because its default data plane is publicly exposed.

### 4. Use a deliberately small flights contract and observable stubs

The Flight schema includes the fields and status enum in the flights capability spec. CreateFlight excludes id and estimated timestamps, and requires flightNumber, origin, destination, scheduledDeparture, and scheduledArrival. UpdateFlight is a partial object for status and estimated timestamps, with at least one property. Contract examples describe flight instances and explicitly show timestamps with offsets.

Keep path and schema validation distinct. A non-empty template segment can route even if it is not a UUID; the Gateway is not a request-schema validator.

Use two small, policy-compliant fixture workloads, flight-query and flight-command, returning distinct backend identities. These are routing stubs, not two production microservices. Choose pinned existing allowlisted images where suitable; if another image is necessary, update the exact repository allowlist, not a registry-wide exemption.

### 5. Explicit routes are the baseline for the later compiler

Use one small HTTPRoute attached only to the dedicated Gateway, with explicit matches:

| Method | Path match | Backend |
| --- | --- | --- |
| GET | Exact /flights | flight-query |
| POST | Exact /flights | flight-command |
| GET | Regex ^/flights/[^/]+$ | flight-query |
| PATCH | Regex ^/flights/[^/]+$ | flight-command |
| GET | Regex ^/flights/[^/]+/status$ | flight-query |

Do not add a catch-all. Query filters such as origin and date belong to the application and are not Gateway matching predicates merely because they appear in OpenAPI.

Verify Envoy path normalization behavior explicitly: empty segments, trailing slashes, encoded slashes, duplicate slashes, and unexpected descendants must not accidentally broaden the documented match boundary. Configure normalization where required for the specified negative cases. Reject unsupported expectations rather than weakening them silently.

OpenAPI QUERY and additionalOperations remain unsupported by these standard HTTPRoute method matches. Any later compiler must surface such limitations explicitly.

### 6. Keep cloud API ownership in the foundation script

Retain the explicit API list in gke-deploy.sh. Envoy running behind ClusterIP introduces no additional Google Cloud API requirement beyond the existing cluster foundation.

Document which APIs are for Compute/GKE and which support the platform's secrets and Workload Identity. Do not add unrelated APIs or invoke the full foundation script as an API-enablement workaround. A disabled GKE API remains a visible deployment blocker until separately resolved with the user.

### 7. Integrate checks without widening privileges

Include first-party fixture manifests in existing Kyverno and Polaris workload inventories. Render the selected chart and inspect its generated controller and proxy resources, including resource requests, security contexts, image repositories, RBAC, and NetworkPolicy connectivity.

Namespace policy overlays must cover the new namespaces with appropriate existing enforcement patterns; do not disable admission policies cluster-wide. Account for dynamically generated proxy Pods, not just checked-in Deployments.

Runtime checks must inspect conditions for the current generation and issue actual HTTP requests. Successful responses must identify the intended backend; negative checks must assert 404 and absence of the fixture identity. Pod readiness alone is insufficient.

## Risks / Trade-offs

- [Existing cluster cannot be verified] -> Stop live installation; retain the plan and report the exact prerequisite. Never run cluster creation as a fallback.
- [CRD versions are cluster-wide] -> Inspect ownership and compatibility before Helm rendering is applied; retain shared definitions on rollback.
- [Candidate release is incompatible] -> Record a compatible supported pin only after observing the retained cluster; otherwise report a block.
- [Proxy defaults create a cloud load balancer] -> Configure and render ClusterIP before creating the Gateway; verify the resulting Service type.
- [GitOps root watches another repository] -> Keep the experiment outside platform reconciliation until an experiment-only source is confirmed.
- [Regex and normalization differ from OpenAPI expectations] -> Assert concrete positive and negative requests against the pinned runtime before treating the fixture as the operator target.
- [Stub responses are mistaken for API enforcement] -> Document the absence of persistence and request/response validation.

## Migration Plan

1. Verify the existing project/cluster identity and access without changing gcloud defaults. Resolve the disabled API prerequisite separately; do not provision infrastructure.
2. Inventory Kubernetes and CRD versions, existing Gateway resources, policies, and available node capacity.
3. Pin compatible Envoy/chart/CRD versions and render manifests before any install.
4. Install the dedicated controller and required CRDs without taking ownership of existing shared definitions.
5. Deploy the ClusterIP Gateway and fixtures, then attach explicit HTTPRoutes.
6. Confirm live resource conditions and run the loopback HTTP acceptance cases.
7. Remove routes, Gateway, fixtures, dedicated configuration, and the Envoy release in dependency order when ending the experiment. Retain shared CRDs, cluster, platform, and unrelated resources. Do not invoke gke-teardown.sh.

## References

- https://gateway.envoyproxy.io/docs/install/install-helm/
- https://gateway.envoyproxy.io/news/releases/matrix/
- https://spec.openapis.org/oas/v3.2.0.html
