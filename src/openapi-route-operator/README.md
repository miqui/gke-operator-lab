# openapi-route-operator

Keeps a Gateway API `HTTPRoute` in line with an OpenAPI 3.2 document. You edit the spec file and
publish it to a ConfigMap; the operator compiles it and applies one generated route, owned by an
`OpenAPIRoute`. Nobody writes or patches the route by hand.

```
ConfigMap (openapi.yaml) ──► OpenAPIRoute ──► compile ──► HTTPRoute (owned) ──► Envoy Gateway
```

- `internal/compiler`: pure translation from document + bindings to a plan. No Kubernetes I/O.
- `internal/controller`: the reconciler (watches, server-side apply, status).
- `api/v1alpha1`: the `OpenAPIRoute` type (`routing.lab.dev/v1alpha1`).

## The OpenAPIRoute API

```yaml
apiVersion: routing.lab.dev/v1alpha1
kind: OpenAPIRoute
metadata:
  name: flights            # the generated HTTPRoute gets this name
  namespace: flights-lab
spec:
  documentRef:
    name: flights-openapi  # ConfigMap in the same namespace
    key: openapi.yaml      # default
  parentRefs:
    - name: flights        # Gateway in the same namespace
  hostnames: [flights.example.test]
  basePath: ""             # optional prefix for every path, e.g. /api (no trailing slash)
  defaultBackendRef: { name: flight-query, port: 8080 }
  operationBindings:       # by operationId; everything else goes to the default
    - operationId: createFlight
      backendRef: { name: flight-command, port: 8080 }
```

| Field | Meaning |
| --- | --- |
| `documentRef` | ConfigMap entry with the OpenAPI 3.2 document (YAML or JSON) |
| `parentRefs` | Gateways the route attaches to; same namespace only |
| `hostnames` | Hostnames the route matches; the document's `servers` are informational |
| `basePath` | Prefix added to every path |
| `defaultBackendRef` | Service and port for operations without a binding |
| `operationBindings` | Per-operationId backend overrides |

### What gets routed

Each operation in `paths` becomes one match with an explicit method. Literal paths are `Exact`.
Whole-segment templates become anchored regexes: `/flights/{flightId}/status` →
`^/flights/[^/]+/status$`. A literal path always wins over a template at the same position.

Compilation fails, and nothing is applied, for any of these:

| Status reason | Examples |
| --- | --- |
| `InvalidDocument` | not YAML/JSON, not `openapi: 3.2.x`, schema violations, unresolved `$ref`, duplicate operationId, undeclared path parameter |
| `UnsupportedRouting` | `query` or `additionalOperations` methods, missing operationId, `{id}.json` partial-segment templates, `//` or dot segments, path- or operation-level `servers`, two templates that can match the same request, external `$ref` |
| `InvalidBinding` | a binding for an operationId not in the document, or a default/binding without a name and port |
| `CapacityExceeded` | more than 128 matches or 16 rules (the HTTPRoute limits) |

Webhooks and callbacks are listed in `status.notProjected` and never routed. The Gateway does not
validate parameter formats: `/flights/not-a-uuid` reaches the backend, which must validate it.

## Status

```console
$ kubectl -n flights-lab get openapiroute
NAME      READY   REASON   ROUTE     AGE
flights   True    Ready    flights   5m
```

| Field | Meaning |
| --- | --- |
| `sourceDigest` | sha256 of the last document read, valid or not |
| `planDigest` | digest of the routing plan currently applied; unchanged by description/schema edits |
| `routeName` | the generated HTTPRoute, empty when nothing is routed |
| `notProjected` | webhooks and callbacks that were deliberately skipped |

| Condition | True when | False reasons |
| --- | --- | --- |
| `SourceValid` | the document compiled (`Compiled`) | `DocumentNotFound`, `DocumentKeyNotFound`, `InvalidDocument`, `UnsupportedRouting`, `InvalidBinding`, `CapacityExceeded` |
| `RouteApplied` | the route matches the plan (`Applied`), or there is nothing to route (`NoOperations`) | `NotApplied` (source invalid; the previous route is kept), `NameConflict` (a route the operator does not own has the name), `ApplyFailed` |
| `DependenciesResolved` | every Service/port and Gateway exists (`Resolved`) | `BackendNotFound`, `GatewayNotFound` |
| `Ready` | the route is Accepted and ResolvedRefs for its current generation, and the Gateway is Programmed | `SourceInvalid`, `NameConflict`, `NoOperations`, `DependenciesMissing`, `Pending`, `RouteNotAccepted`, `RefsNotResolved`, `GatewayNotProgrammed` |

Ready is about configuration, not reachability: only real HTTP requests prove a backend answers.

## Changing the API safely

- **Invalid edits are harmless.** If a published document does not compile, the existing route
  keeps serving and `SourceValid` says why. Fix the document and publish again.
- **Description or schema edits** update `sourceDigest` only; the route is not rewritten.
- **Removing a bound operation**: remove it from the spec first. That edit is rejected
  (`InvalidBinding`) while the binding still names it, so the current route keeps serving. Then
  remove the binding, which applies. Removing the binding first would briefly send the operation to
  the default backend.
- **Never edit the generated route.** The operator reverts manual changes and recreates it if
  deleted.

## Development

Requires Go (see `go.mod`), Docker, and access to the cluster for `make run`.

```sh
make test     # unit tests + envtest (Kubernetes 1.35 API server; binaries pinned by the Makefile)
make build    # bin/manager
make lint     # golangci-lint
```

`make manifests generate` regenerates the CRD, RBAC, and DeepCopy code from the Go types, and
copies the go.mod-pinned Gateway API CRDs into `test/crds/gateway-api`. CI fails if the
committed output differs.

### Running against the dev cluster

Every instance takes the same leader-election lease in `flights-lab`, so a local run waits while
the deployed operator holds it. Scale the deployment down first:

```sh
kubectl --context gke_dev1-486920_us-central1-a_dev-cluster -n flights-lab \
  scale deployment openapi-route-operator-controller-manager --replicas=0
make install KUBE_CONTEXT=gke_dev1-486920_us-central1-a_dev-cluster   # the CRD
make run KUBE_CONTEXT=gke_dev1-486920_us-central1-a_dev-cluster
```

Scale back to 1 when done. The flags are `--kube-context`, `--watch-namespace` (default
`flights-lab`), and `--leader-elect` (default true).
