# Tasks

## 1. Existing-cluster prerequisites

- [ ] 1.1 Verify access to the retained cluster in dev1-486920 and record its name, exact location within us-central1, and Kubernetes version using project-scoped read-only commands; stop on disabled APIs or an unverifiable target without provisioning anything.
- [ ] 1.2 Document gke-deploy.sh's explicit Compute, GKE, Secret Manager, IAM, and IAM Credentials API dependencies and verify the script enables this bounded list before provisioning; do not add unrelated APIs or execute full provisioning as an API-only workaround.
- [ ] 1.3 Inventory existing Gateway resources and CRD versions, channels, and owners; record a compatible supported Envoy/chart/CRD version pin in HELM.md and installation values, or report the precise compatibility block.

## 2. Isolated Envoy runtime

- [ ] 2.1 Add k8s/envoy-gateway installation values and an explicit CRD ownership procedure; render the pinned charts and verify provider-managed/shared CRDs are neither overwritten nor adopted.
- [ ] 2.2 Add the dedicated namespaces, flights-envoy GatewayClass, EnvoyProxy configuration, and Gateway; verify rendered resources set the proxy Service to ClusterIP before Gateway creation and restrict route attachment to flights-lab.
- [ ] 2.3 Apply existing resource/security/image policy patterns to the controller and generated proxy, updating exact image repositories and namespace coverage only as needed; verify chart rendering and applicable policy checks without cluster-wide exemptions.
- [ ] 2.4 Document isolated installation, current-context safeguards, loopback port-forwarding, and bounded removal in the exercise documentation; verify all commands avoid platform root changes, public Services, and gke-teardown.sh.

## 3. Flights contract and fixtures

- [ ] 3.1 Add the OpenAPI 3.2.0 flights contract with Flight, CreateFlight, and UpdateFlight schemas and all five operationIds; verify it with a 3.2-capable validator and check representative examples against the declared schemas.
- [ ] 3.2 Add policy-compliant flight-query and flight-command stubs and Services with distinguishable responses; verify rendered manifests and direct fixture responses identify the correct backend.
- [ ] 3.3 Add explicit method/path HTTPRoute matches for all five operations with no catch-all; verify exact collection matches, anchored single-segment regexes, same-namespace backend references, and attachment only to the experiment Gateway.
- [ ] 3.4 Add fixture workloads to the existing Kyverno and Polaris inventories and document stub limitations; verify focused policy checks and ensure the documentation does not claim persistence, schema enforcement, or arbitrary HTTP-method support.

## 4. Routing acceptance and removal

- [ ] 4.1 Add repeatable routing checks that assert controller/Gateway readiness, current-generation HTTPRoute acceptance and resolved references, response status, and backend identity; verify failures are explicit and processes use loopback-only forwarding.
- [ ] 4.2 Run positive traffic checks for all five operations through Envoy and verify GET and POST /flights reach different backends.
- [ ] 4.3 Run negative checks for unsupported methods, unrelated Host headers, collection prefixes, empty segments, trailing slashes, and extra descendants; verify Gateway 404 responses without fixture backend identity, explicitly configuring path normalization if needed.
- [ ] 4.4 Exercise installation reapplication and document experiment-only removal; verify stable resource ownership and that shared CRDs, existing platform resources, and the GKE cluster are retained.
