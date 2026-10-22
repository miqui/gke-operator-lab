# Tasks

## 1. Cluster foundation

- [x] 1.1 Add `artifactregistry.googleapis.com` to the API list in `gke-deploy.sh`, and add an `exists`-guarded `operator-lab` Docker repository in `us-central1` with a repository-scoped `roles/artifactregistry.reader` binding for `gke-dev-nodes`. Update the node-SA "every image is public" comment. Verify with `bash -n gke-deploy.sh`.
- [x] 1.2 Add `--gateway-api=disabled` to the `clusters create` command after confirming the flag with `gcloud container clusters create --help`. Verify with `bash -n`.
- [x] 1.3 Run `PROJECT_ID=dev1-486920 ./gke-deploy.sh` after explicit user go-ahead, since it creates billable resources. Verify that `kubectl get nodes` shows 3 Ready nodes, that `kubectl get crd | grep gateway.networking.k8s.io` returns nothing, and that `gcloud artifacts repositories get-iam-policy operator-lab` shows only the reader binding.
- [x] 1.4 Record the cluster's Kubernetes version and update GKE-OPS.md and KUBECTL.md with the commands actually run. Verify that the documented `get-credentials` command works from a fresh shell.

## 2. Operator scaffold and CI

- [x] 2.1 Pin the Go toolchain and install Kubebuilder and setup-envtest at pinned versions. Verify that `go version`, `kubebuilder version`, and a responsive `docker info` succeed.
- [x] 2.2 Scaffold `src/openapi-route-operator/` (Kubebuilder go/v4, domain `lab.dev`, group `routing`, kind `OpenAPIRoute`, version `v1alpha1`) and add the Gateway API module at the pinned version. Add Go build/test exclusions to `.gitignore`. Verify that `make build` and `make test` pass on the scaffold.
- [x] 2.3 Add `.github/workflows/operator-ci.yml`, filtered to the module path, running gofmt/vet, `make manifests generate` plus a clean `git diff --exit-code`, `make test`, and `make docker-build`. Verify by running the same steps locally with no diff.

## 3. Flights contract and compiler

- [x] 3.1 Write `examples/flights/openapi.yaml` (OpenAPI 3.2.0, the five operations, and the Flight, CreateFlight, UpdateFlight, and FlightStatus schemas, with coherent examples) per the flights-routing-fixture spec. Verify that it validates against the official OAS 3.2 JSON Schema.
- [x] 3.2 Run the parser spike: load the contract plus 3.2 fixtures (`query`, `additionalOperations`, `$self`, webhooks, callbacks, internal and external `$ref`) with libopenapi in a throwaway test. Record the outcome in design.md decision 2 as either "adopted" or "fallback". Verify that the test shows every operation is visible and that external refs are refused.
- [x] 3.3 Implement document loading and validation in `internal/compiler` with typed invalid-document errors. Verify with unit tests for malformed YAML/JSON, the wrong version, unresolved refs, and external refs.
- [x] 3.4 Implement operation extraction: path items, internal `$ref` path items, required path parameters, operationId presence and uniqueness, and webhook/callback reporting. Verify with unit tests that cover each rejection and the non-projected list.
- [x] 3.5 Implement match generation: Exact for literal paths, escaped and anchored single-segment regexes, basePath, explicit methods, and rejection of unsupported features (QUERY, additionalOperations, partial segments, server overrides and variables). Verify with unit tests, including the flights baseline plan and regex escaping.
- [x] 3.6 Implement precedence and ambiguity checks, binding resolution (defaultBackendRef plus operationBindings, failing on unknown operationIds), and grouping by backend into rules. Verify with unit tests for literal-vs-template and ambiguous templates.
- [x] 3.7 Implement deterministic sorting, the plan digest over routing content only, empty-plan handling, and capacity checks parameterized by limits. Verify with unit tests: reordered input gives an identical plan, a description-only edit gives the same digest, a removal changes the digest, an empty API gives an empty plan, and overflow gives a capacity error. Add the test that asserts the limits equal the vendored CRD's `maxItems` and CEL bounds.

## 4. OpenAPIRoute API and reconciler

- [x] 4.1 Define `OpenAPIRoute` spec and status types with validation markers (required `documentRef` and `defaultBackendRef`, a pattern for `basePath`) and the status subresource. Verify that `make manifests` regenerates the CRD and that envtest rejects an object missing a required field.
- [x] 4.2 Implement the reconciler: fetch inputs, compile, run the ownership pre-check, delete on an empty plan, skip writes by plan-digest annotation, and server-side apply with the `openapi-route-operator` field manager. Verify with envtest: create gives an owned route, a ConfigMap-only update changes the route, a description-only update leaves the route generation unchanged, and a foreign object at the name gives a NameConflict with the object untouched.
- [x] 4.3 Add watches and field indexes for ConfigMap (`documentRef.name`), Services, and Gateways, scoped to the watched namespace. Verify with envtest that a ConfigMap edit and a Service creation each trigger a reconcile.
- [x] 4.4 Implement failure preservation and drift repair. Verify with envtest that an invalid edit keeps the prior route and sets `SourceValid=False`, that an invalid first input creates no route, that a manual spec edit is reverted, and that a deleted route is recreated.
- [x] 4.5 Implement status: the digests, observedGeneration, the route name, and the `SourceValid`, `RouteApplied`, `DependenciesResolved`, and `Ready` conditions, with Ready derived from the route's Accepted and ResolvedRefs for the current generation plus Gateway Programmed. Verify with envtest by writing HTTPRoute and Gateway status by hand, checking that a missing Service gives `DependenciesResolved=False` and that Ready flips only when all inputs agree.
- [x] 4.6 Configure leader election (lease `openapi-route-operator` in `flights-lab`, shared by `make run` and the Deployment), health and readiness probes, and namespaced RBAC markers with no Secret access. Verify that the generated Role has no `secrets` rule and that a second manager blocks on the lease in envtest or a local run.
- [x] 4.7 Verify restart recovery: stop and start the envtest manager after drift and confirm convergence. Also confirm that the ownerReferences on the route point at the OpenAPIRoute with `controller: true`.
- [x] 4.8 Document the API fields, conditions and reasons, and the local `make run` loop (scale the Deployment to zero first) in the module README. Verify that the documented commands run as written.

## 5. Envoy Gateway runtime

- [x] 5.1 Choose the Envoy Gateway and Gateway API pins compatible with the Kubernetes version from 1.4 (candidate v1.9.2 / v1.6.1). Record them in HELM.md. Verify against the published compatibility matrix.
- [x] 5.2 Add `k8s/envoy-gateway/` with the CRD-chart and controller-chart (`--skip-crds`) values and an install script that runs a preflight refusing to proceed if GKE-managed Gateway API CRDs exist. Verify that the preflight fails against a test CRD carrying GKE labels and passes on the real cluster.
- [x] 5.3 Install the CRDs and controller. Verify that the controller Deployment is Available and that the CRD versions match the pins.
- [x] 5.4 Add the `EnvoyProxy` (ClusterIP), GatewayClass `flights-envoy` with `parametersRef`, the Gateway in `flights-lab` (hostname `flights.example.test`, allowedRoutes Same), and the `ClientTrafficPolicy` (disable merge slashes, reject escaped slashes). Verify that the GatewayClass is Accepted, that the Gateway is Programmed, that the proxy Service is ClusterIP, and that no LoadBalancer Service exists in any namespace for it.
- [x] 5.5 Document port-forward access (loopback `127.0.0.1:8083`, Host header) and exercise removal order in KUBECTL.md and HELM.md. Verify that the documented port-forward reaches Envoy, which returns 404 with no routes.

## 6. Flights fixtures and publication

- [x] 6.1 Add `k8s/flights/` with the namespace, the pinned nginx echo ConfigMap, the flight-query and flight-command Deployments and Services, and a kustomization (excluding the OpenAPIRoute until 7.3). Verify with `kubectl apply -k k8s/flights` and by checking that a direct in-cluster request with any method returns the backend's identity JSON.
- [x] 6.2 Add repo-root `flights-publish.sh` (explicit `--context`, create-configmap dry-run piped to apply). Verify that two runs with an edited file keep the same ConfigMap name and update its data.
- [x] 6.3 Document the fixtures' limitations (no persistence, no auth, no schema validation, no parameter-format validation by the Gateway) in README.md. Verify that the statements are present.

## 7. Image and operator deployment

- [x] 7.1 Add a `make docker-push` path using `docker buildx build --platform linux/amd64 --push` to the Artifact Registry repository tagged with the git SHA. Verify with `docker buildx imagetools inspect` that the image shows `linux/amd64`.
- [x] 7.2 Add `us-central1-docker.pkg.dev/dev1-486920/operator-lab/openapi-route-operator` to `restrict-image-repositories.yaml`. Verify that `./check-policies.sh` passes, including the known-bad fixture.
- [x] 7.3 Add `k8s/operator/` (CRD, RBAC, Deployment by immutable tag with leader election and probes) and the sample OpenAPIRoute (default flight-query, with createFlight and updateFlight bound to flight-command) to `k8s/flights/`. Verify that the operator Pod is Ready with no image-pull Secret.
- [x] 7.4 Publish the spec and apply the OpenAPIRoute. Verify that the HTTPRoute is created, that `Ready=True`, and that a manual curl of GET and POST `/flights` through the port-forward hits the expected backends.

## 8. GKE acceptance suite

- [x] 8.1 Add `test/e2e` scaffolding: explicit `KUBE_CONTEXT`, its own loopback port-forward on a random port, the Host header, bounded `Eventually`, diagnostics capture to gitignored `test/e2e/artifacts/`, and `make test-e2e`. Verify that the suite fails rather than skips when the context is missing.
- [x] 8.2 Add the testdata spec variants (baseline, plus-delete, rebound, minus-operation, invalid, unsupported-query, description-only, literal-vs-template, empty) and the scenarios from the operator-delivery spec. Verify that all scenarios pass against the dev cluster.
- [x] 8.3 Add the negative-traffic, drift and restart, ownership (unrelated route untouched, name collision), readiness-failure (broken Service reference), and cleanup (OpenAPIRoute deletion garbage-collects the route) scenarios. Verify that all pass, and that a deliberately broken assertion produces diagnostics.
- [x] 8.4 Document the acceptance command, its prerequisites, and how to read failures in README.md. Verify that the command runs as documented from a fresh shell.

## 9. Integration and wrap-up

- [ ] 9.1 Run the full sequence from the design's migration plan on the dev cluster, from the CRD preflight through `make test-e2e`. Verify that everything passes in one run, and record the operator image digest and versions in README.md.
- [ ] 9.2 Walk through the APIOps example from PLAN.md by hand (add `DELETE /flights/{flightId}`, bind it, publish, curl). Verify that the request reaches flight-command.
- [ ] 9.3 Remove the superseded `openspec/changes/add-envoy-flights-gateway/` change. Verify that `openspec list` shows only this change and that `openspec validate add-openapi-route-operator --strict` passes.

## Workflow follow-up

- Archive this change after review, then verify that the six capability specs appear under `openspec/specs/`.
- Phase 2 hardening (policy coverage, workload security, NetworkPolicies, GitOps ownership) is a separate change.
