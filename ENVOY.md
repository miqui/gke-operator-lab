# Envoy Gateway — the OpenAPI route operator's routing target

Everything about the Envoy Gateway runtime this lab uses for the OpenAPI route operator
exercise: what is installed and why, how traffic reaches it, which Envoy behaviors the operator
relies on (and how each was verified on the dev cluster), how to troubleshoot it, and how to
remove or upgrade it. The operator itself is documented in
[its README](src/openapi-route-operator/README.md); the exercise as a whole in
[README.md](README.md) "OpenAPI route operator".

```text
examples/flights/openapi.yaml ──flights-publish.sh──► ConfigMap flights-openapi
                                                        │
                       OpenAPIRoute flights ──operator──┤
                                                        ▼
                                            HTTPRoute flights (generated)
                                                        │ Envoy Gateway controller
                                                        ▼
127.0.0.1:8083 ──port-forward──► Envoy proxy (ClusterIP) ──► flight-query / flight-command
```

## What is installed

| Piece | Where | Notes |
| --- | --- | --- |
| Gateway API CRDs v1.6.1, **standard channel** | cluster-wide | from `gateway-crds-helm`, server-side applied (field manager `eg-crds`) |
| Envoy Gateway CRDs (`gateway.envoyproxy.io`) | cluster-wide | same chart and apply |
| Envoy Gateway controller v1.9.2 | `envoy-gateway-system`, Helm release `eg` | `gateway-helm`, `--skip-crds`, chart defaults |
| `EnvoyProxy flights-clusterip` | `envoy-gateway-system` | makes proxy Services ClusterIP |
| `GatewayClass flights-envoy` | cluster-wide | controller `gateway.envoyproxy.io/gatewayclass-controller`, `parametersRef` → the EnvoyProxy |
| `Gateway flights` | `flights-lab` | one HTTP listener, port 80, hostname `flights.example.test`, routes from the same namespace only |
| `ClientTrafficPolicy flights-paths` | `flights-lab` | path normalization the operator's matches rely on (below) |
| Envoy proxy Deployment + Service | `envoy-gateway-system`, generated (`envoy-flights-lab-flights-<hash>`) | created by the controller for the Gateway |

Files: `k8s/envoy-gateway/` (`install.sh`, `crds-values.yaml`, `values.yaml`, `gatewayclass.yaml`)
and `k8s/flights/gateway.yaml` (Gateway, ClientTrafficPolicy).

### Versions and why

Checked 2026-10-22 against the [compatibility
matrix](https://gateway.envoyproxy.io/news/releases/matrix/):

| Envoy Gateway | Envoy Proxy | Gateway API | Kubernetes |
| --- | --- | --- | --- |
| **v1.9** (installed: v1.9.2) | distroless-v1.39.x | **v1.6.1** | v1.33 – v1.36 |
| v1.8 | distroless-v1.38.x | v1.5.1 | v1.32 – v1.35 |

The dev cluster runs v1.35.6 ([GKE-OPS.md](GKE-OPS.md) "Cluster facts"). Envoy Gateway v1.9.2's own
`go.mod` pins `sigs.k8s.io/gateway-api v1.6.1` and `k8s.io/api v0.37.0`; the operator pins the same
two, and its envtest loads the same standard-channel v1.6.1 CRDs (`make gateway-api-crds`). The
operator, its tests, and the cluster therefore agree on one Gateway API.

**Standard channel only.** `gateway-crds-helm` defaults to the experimental channel and to
installing no Gateway API CRDs at all; `crds-values.yaml` sets `gatewayAPI.enabled: true,
channel: standard`. The operator emits only standard `HTTPRoute` fields.

## Installing

```bash
./k8s/envoy-gateway/install.sh                    # preflight, CRDs, controller, GatewayClass, verify
./k8s/envoy-gateway/install.sh --preflight-only   # just the CRD ownership check
KUBE_CONTEXT=... ./k8s/envoy-gateway/install.sh   # default: gke_dev1-486920_us-central1-a_dev-cluster
```

Then `kubectl apply -k k8s/flights` creates the Gateway and its ClientTrafficPolicy (and the
fixtures). The script is idempotent; re-running it is the upgrade path.

### Who owns the Gateway API CRDs

The lab does. GKE's managed Gateway API is disabled at cluster creation
(`gke-deploy.sh --gateway-api=disabled`, verified as `CHANNEL_DISABLED`), so GKE never installs or
reverts these CRDs. Before installing, `install.sh` lists every `*.gateway.networking.k8s.io` CRD
and **refuses to continue if any lacks the `eg-crds` field manager** in its `managedFields` — that
covers GKE-managed CRDs and anything installed by another tool, without needing to know their
labels. CRDs are cluster-wide; overwriting someone else's would break their routes.

### Why the CRDs are not a Helm release

Two problems found on the dev cluster, both handled in `install.sh`:

1. **Release Secret too large.** `helm install eg-crds …` failed with
   `Secret "sh.helm.release.v1.eg-crds.v1" is invalid: data: Too long: may not be more than 1048576 bytes`:
   Helm stores the rendered bundle in a release Secret, and v1.9.2's CRDs exceed Kubernetes' 1 MiB
   object limit. Envoy Gateway documents `helm template … | kubectl apply --server-side` instead,
   which is what the script does (field manager `eg-crds`, which is also the ownership marker the
   preflight checks).
2. **Helm v4 writes to stdout while rendering from a registry.** `helm template oci://…` prints
   `Pulled:` and `Digest:` lines on stdout, which corrupted the manifest stream
   (`error validating data: [apiVersion not set, kind not set]`). The script `helm pull`s the chart
   into a temp directory first and renders the local `.tgz`.

Nothing was half-applied by either failure: Helm failed before creating resources, and `kubectl`
validates its whole input before applying any of it.

## Reaching the Gateway

The proxy Service is **ClusterIP**. There is no Ingress, LoadBalancer Service, DNS record, or
certificate; verified on the cluster: zero LoadBalancer Services, zero GCP forwarding rules, no
`EnsuringLoadBalancer` event ever. The EnvoyProxy sets ClusterIP *before* any Gateway of the class
exists, so a public load balancer is never created even transiently.

Access is a loopback port-forward with the listener's hostname. The Service name is generated, so
find it by its Gateway labels:

```bash
K=gke_dev1-486920_us-central1-a_dev-cluster
SVC=$(kubectl --context $K -n envoy-gateway-system get svc -o name \
  -l gateway.envoyproxy.io/owning-gateway-namespace=flights-lab,gateway.envoyproxy.io/owning-gateway-name=flights)
kubectl --context $K -n envoy-gateway-system port-forward --address 127.0.0.1 "$SVC" 8083:80

curl -i -H 'Host: flights.example.test' http://127.0.0.1:8083/flights
```

Service port 80 maps to container port **10080**: Envoy Gateway shifts privileged listener ports by
10000 so the proxy runs unprivileged (`Forwarding from 127.0.0.1:8083 -> 10080`). Without the
`Host` header every request is a 404: the listener only matches `flights.example.test`.

## Envoy behaviors the operator relies on

The operator compiles each OpenAPI operation into an HTTPRoute match with an explicit method:
literal paths as `Exact`, whole-segment templates as anchored regexes (`/flights/{flightId}` →
`^/flights/[^/]+$`). These behaviors are implementation-specific or configurable, so each was
checked against real Envoy and is locked in by the GKE acceptance suite (`make test-e2e`).

| Behavior | Why it matters | Verified |
| --- | --- | --- |
| **Exact beats RegularExpression** | Gateway API leaves regex precedence to the implementation. A literal path (`/flights/search`) must win over a template at the same position (`/flights/{flightId}`). | `/flights/search` → its binding; `/flights/abc` → the template's |
| **Regex matches the whole path (RE2, full match)** | `[^/]+` must be exactly one segment; anchors are redundant but kept for clarity. | `…/status/extra`, `/flights-extra` → 404 |
| **Method is part of the match** | Same path, different backend per method; undeclared methods must not fall through. | POST `/flights` → flight-command, PUT → 404 |
| **No match → 404 from the gateway** | Removing an operation must make it unreachable; there is no catch-all. | removed op → 404; empty API → route deleted, 404 |
| **Query string is ignored for path matching** | OpenAPI query parameters are application input, not route predicates. | `/flights?origin=YYZ` → flight-query |
| **Routes attach only from `flights-lab`** | `allowedRoutes: Same`; other namespaces cannot hijack the listener. | listener config |

### Path normalization (ClientTrafficPolicy `flights-paths`)

```yaml
path:
  disableMergeSlashes: true
  escapedSlashesAction: RejectRequest
```

- **Duplicate slashes.** By default Envoy merges them, so `/flights//status` would become
  `/flights/status`, match `^/flights/[^/]+$`, and reach **getFlight** with `flightId=status`.
  With merging disabled it is a 404. Verified: `/flights//status` → 404, `//flights` → 404.
- **Encoded slashes.** `%2F` could smuggle a second segment past `[^/]+`. With
  `RejectRequest`, `/flights/a%2Fb/status` → **400** from Envoy, never forwarded.
- **Trailing slash.** Not normalized: `/flights/` is a different path from `/flights` and is a 404
  unless the document declares it.

The operator's compiler also refuses to emit paths that need normalization (`//`, `.`/`..`
segments, `%2F`, characters outside RFC 3986 `pchar`), matching the HTTPRoute CRD's own
validation rules for `Exact` paths.

### Status the operator reads

Envoy Gateway writes `status.parents[]` on each attached HTTPRoute (`Accepted`, `ResolvedRefs`, with
`observedGeneration`) and `Programmed` on the Gateway. The OpenAPIRoute is `Ready` only when both
route conditions are True **for the route's current generation** and the Gateway is Programmed
for its own. `Programmed` on HTTPRoutes is not portable, so it is not required.

Envoy Gateway writes this status on **every** route attached to the Gateway, including routes the
operator does not own, which changes their `resourceVersion`. "The operator never touched it" is
therefore checked via `managedFields` (no `openapi-route-operator` manager), generation, labels,
annotations, and owner references, never `resourceVersion`. (The acceptance suite's first run
failed on exactly this.)

A route whose backend Service is missing is still accepted with `ResolvedRefs=False`; Envoy answers
those requests with 500. The operator reports it as `DependenciesResolved=False` and `Ready=False`.

## Troubleshooting

```bash
kubectl --context $K get gatewayclass flights-envoy                          # ACCEPTED True
kubectl --context $K -n flights-lab get gateway flights                       # PROGRAMMED True
kubectl --context $K -n flights-lab get httproute -o wide
kubectl --context $K -n flights-lab get httproute flights \
  -o jsonpath='{range .status.parents[*].conditions[*]}{.type}={.status} gen={.observedGeneration} {.reason}{"\n"}{end}'
kubectl --context $K -n flights-lab get clienttrafficpolicy flights-paths \
  -o jsonpath='{.status.ancestors[*].conditions[?(@.type=="Accepted")].status}'
kubectl --context $K -n envoy-gateway-system logs deploy/envoy-gateway --tail=100   # controller
kubectl --context $K -n envoy-gateway-system logs -l gateway.envoyproxy.io/owning-gateway-name=flights \
  -c envoy --tail=100                                                         # proxy access/errors
```

| Symptom | Likely cause |
| --- | --- |
| Every request 404, even `/flights` | missing `Host: flights.example.test`; or no HTTPRoute (check the OpenAPIRoute's `SourceValid`/`RouteApplied`) |
| One operation 404 | not in the published document, or its method/path shape differs (trailing slash, extra segment) |
| 500 for one operation | its backend Service or port is missing (`ResolvedRefs=False` on the route) |
| 400 | encoded slash in the path (by design) |
| Route not `Accepted` | parentRef wrong, or listener hostname mismatch; see `status.parents` |
| `install.sh` preflight fails | Gateway API CRDs from another owner exist; check the cluster's `gatewayApiConfig.channel` |
| `kubectl` times out | control-plane authorized network; see GKE-OPS.md |

## Removing

In dependency order; the cluster and the platform stay:

```bash
kubectl --context $K -n flights-lab delete openapiroute --all     # its HTTPRoutes are garbage-collected
kubectl --context $K -n flights-lab get httproute                 # confirm: none left
kubectl --context $K delete -k k8s/flights                        # Gateway, policy, fixtures, namespace
kubectl --context $K delete -k k8s/operator                       # operator (its CRD stays)
kubectl --context $K delete -f k8s/envoy-gateway/gatewayclass.yaml
helm --kube-context $K -n envoy-gateway-system uninstall eg
```

Deliberately separate, because each deletes **every** object of its kinds cluster-wide:

```bash
kubectl --context $K delete crd openapiroutes.routing.lab.dev     # every OpenAPIRoute
# Gateway API + Envoy Gateway CRDs (pull first: Helm v4 prints "Pulled:" to stdout otherwise)
helm pull oci://docker.io/envoyproxy/gateway-crds-helm --version v1.9.2 --destination /tmp/eg-crds
helm template eg-crds /tmp/eg-crds/gateway-crds-helm-v1.9.2.tgz \
  --values k8s/envoy-gateway/crds-values.yaml | kubectl --context $K delete -f -
```

## Upgrading

Bump together, in one change:

1. `EG_VERSION` in `k8s/envoy-gateway/install.sh` and the version comments in `crds-values.yaml` /
   `values.yaml`, after re-checking the compatibility matrix against the cluster's Kubernetes version.
2. `sigs.k8s.io/gateway-api` in the operator's `go.mod` to the new release's Gateway API version
   (read it from Envoy Gateway's own `go.mod` at that tag).
3. `make manifests` (re-copies the CRDs for envtest; CI fails on drift), `make test` — the limits
   test fails if HTTPRoute's rule/match limits changed — then `./k8s/envoy-gateway/install.sh` and
   `make test-e2e`.

The path normalization rows above are the ones most likely to change behavior across Envoy
versions; the acceptance suite's negative-traffic specs cover them.

## References

- Envoy Gateway: [install with Helm](https://gateway.envoyproxy.io/docs/install/install-helm/),
  [compatibility matrix](https://gateway.envoyproxy.io/news/releases/matrix/)
- [Gateway API HTTPRoute](https://gateway-api.sigs.k8s.io/api-types/httproute/) (matching and precedence)
- [PLAN.md](PLAN.md) and `openspec/changes/add-openapi-route-operator/design.md` (decision 9)
