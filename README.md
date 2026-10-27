# gke-operator-lab

A lab for testing [Envoy Gateway](https://gateway.envoyproxy.io/) routing driven by an
**OpenAPI 3.2** contract, on a dev [GKE](https://cloud.google.com/kubernetes-engine) cluster in GCP
project **`dev1-486920`**. A custom operator compiles the contract into a Gateway API HTTPRoute:
edit `examples/flights/openapi.yaml`, publish it, and Envoy routes requests by the new contract
without anyone touching the route. A GKE acceptance suite proves that spec edits change live
traffic. See [OpenAPI route operator](#openapi-route-operator-flights-exercise).

Underneath is a reusable platform: Argo CD deploys it, and Crossplane can provision GCP resources
needed by experiments.

> **Status:** the platform, the operator (Kubebuilder, with CI) and the GKE acceptance suite are
> in place; the exercise was verified end to end on 2026-10-22 (see
> [Verified deployment](#verified-deployment-2026-10-22)). New, unrelated projects start as a fork
> of this repo. It started as a copy of `miqui/gke-springboot-grpc-o2`. The Java gRPC service,
> Cloud SQL, Hazelcast, the public Gateway and the Grafana/Prometheus/OpenObserve stack were
> removed. That repo and its GCP project (`k8s-dev-412419`) are untouched.

The cluster is meant to be **short-lived**: create it for an experiment, tear it down after a few
hours, recreate it for the next one. Secrets and service accounts live outside it and are reused.

## Stack URLs

Nothing is public: there is no Ingress or LoadBalancer, and the exercise's Envoy Gateway proxy is
ClusterIP ([ENVOY.md](ENVOY.md)). Every UI is reached through
`./gke-port-forward.sh`. It tunnels through the GKE API server, which only accepts your IP plus
Google IAM, and binds on `127.0.0.1` only.

| Component | URL | Login |
| --- | --- | --- |
| Argo CD | `https://localhost:8081` (self-signed cert) | `admin` / `kubectl -n argocd get secret argocd-initial-admin-secret -o jsonpath='{.data.password}' \| base64 -d`. Change it in the UI, then delete that Secret. |
| Headlamp | `http://localhost:4466` | `kubectl create token headlamp -n headlamp --duration=1h \| tr -d '\n' \| pbcopy` (read-only) |
| Polaris | `http://localhost:8082` | none (read-only report) |

## Architecture

- **Project isolation**: every script takes `PROJECT_ID` (required, no default) and exports it as
  `CLOUDSDK_CORE_PROJECT`. All `gcloud` calls are therefore pinned to that project, and your
  gcloud configuration's default project is never changed. `gke-bootstrap.sh` refuses to run if
  the manifests in `k8s/` are not for the given project.
- **Cluster** (`gke-deploy.sh`): zonal GKE Standard cluster `dev-cluster` in `us-central1-a`,
  `e2-standard-2` nodes autoscaling 3-5, release channel `stable`.
  - **Private nodes** (no external IPs), with egress through Cloud NAT. Third-party images are
    public (ghcr.io, Docker Hub, quay.io, ...). First-party images (the operator) come from the
    Artifact Registry repository `operator-lab` in `us-central1`, pinned by digest; the node
    service account can read that repository only. The control plane only accepts the operator's
    public IP (master authorized networks).
  - **Dataplane V2** enforces NetworkPolicies. **Workload Identity** gives in-cluster controllers
    Google credentials without key files. Shielded Nodes with secure boot. GKE Managed
    Prometheus is off.
  - Nodes run as a dedicated `gke-dev-nodes` service account with only
    `roles/container.defaultNodeServiceAccount`, not the default compute account (which has
    project Editor).
  - A firewall rule lets the control plane reach admission webhooks on 8443/9443 (Kyverno,
    Crossplane) on older peering-based private clusters. Current PSC-based clusters need no rule.
- **GitOps**: Argo CD syncs the whole platform from this repo's `main` branch; see
  [Continuous Deployment with Argo CD](#continuous-deployment-with-argo-cd).
- **GCP resources through Crossplane** (`k8s/platform/`): the GCP provider family plus its
  functions, and a `ClusterProviderConfig` that authenticates as the `crossplane-gcp` service
  account via Workload Identity. No managed resources exist yet. To add one, put a sub-provider
  (e.g. `provider-gcp-storage`) in `crossplane-packages.yaml` with
  `runtimeConfigRef: {name: provider-gcp}`, add an XRD + Composition, and grant `crossplane-gcp`
  the narrowest matching role in `gke-deploy.sh`.
- **Secrets**: nothing secret is committed, or passed through a script into the cluster.
  - Values live in 1Password. `gke-secrets-seed.sh` copies them into **GCP Secret Manager**.
  - The **External Secrets Operator** builds Kubernetes Secrets from `ExternalSecret` manifests.
    It authenticates as the `external-secrets` service account via Workload Identity, and can read
    exactly the seeded secrets.
  - No secret store or secrets are defined yet. Before adding an `ExternalSecret`, add a
    `ClusterSecretStore` in `k8s/platform/` with namespace-scoped access conditions.
    Each secret is an entry in `gke-secrets-seed.sh`, `.env.example` and `REQUIRED_SECRETS` in
    `gke-bootstrap.sh`.
- **Tools**:
  - **[Headlamp](https://headlamp.dev/)** is a Kubernetes web UI, bound to the read-only `view`
    ClusterRole.
  - **[Polaris](https://polaris.docs.fairwinds.com/)** is a best-practice dashboard with read-only
    RBAC.
  - **[Trivy Operator](https://github.com/aquasecurity/trivy-operator)** does continuous CVE and
    misconfiguration scanning ([TRIVY.md](TRIVY.md)).

## Continuous Deployment with Argo CD

`gke-bootstrap.sh` does two things by hand: it Helm-installs Argo CD and applies one **root**
Application (`k8s/argocd/root-application.yaml`). The root Application syncs every other
Application from `k8s/argocd/apps/` ("app of apps"), Argo CD itself included. From then on, a
version bump, a values change or a new manifest is a merged commit to
`https://github.com/miqui/gke-operator-lab.git`. The repo must be public, because Argo CD clones it
anonymously.

| Wave | Application | Source |
| --- | --- | --- |
| -4 | `argocd` (self-managed), `external-secrets`, `crossplane` | upstream charts + values in `k8s/argocd/`, `k8s/external-secrets/`, `k8s/crossplane/` |
| -3 | `kyverno`, `platform` | Kyverno chart; `k8s/platform/` (Crossplane GCP provider family/functions/config) |
| -2 | `kyverno-policies` | `k8s/policies/`: enforce rules exist before any workload syncs |
| -1 | `headlamp`, `polaris`, `trivy-operator` | charts + values |

A wave only starts once the previous one is Healthy. That depends on the Application health check
in `k8s/argocd/argocd-values.yaml`, which also teaches Argo CD to read Crossplane's Ready
conditions. Every Application runs `automated: {prune: true, selfHeal: true}`, except Argo CD's
own, which doesn't prune. Day-to-day commands are in [ARGOCD.md](ARGOCD.md), and chart versions
in [HELM.md](HELM.md).

## Policy as Code with Kyverno

[Kyverno](https://kyverno.io/) runs admission control in the cluster. The *same* policies are
checked in CI (`.github/workflows/policy-check.yml` → `check-policies.sh`). They use the CEL-based
`policies.kyverno.io/v1` types.

- `k8s/policies/rules/` holds each rule once:
  - `disallow-host-access`
  - `require-secure-container-context`
  - `require-resources`
  - `restrict-image-repositories`, an exact-repository allowlist
- Overlays turn the rules into a **Deny** copy for `default` and an **Audit** copy for `headlamp`
  and `polaris`. `audit-only/` holds `disallow-latest-tag` and `restrict-cluster-admin-bindings`.
- **A new image** needs its repository in `restrict-image-repositories.yaml`.
- **A new namespace** goes into the overlays' `namespaceSelector`.
- `check-policies.sh` evaluates the enforce and audit sets against this repo's own workload
  manifests (`WORKLOAD_DIRS`, empty for now). It also requires the enforce set to reject a
  known-bad fixture. `polaris-audit.sh` runs the Polaris checks against the same manifests,
  report-only.

Debugging commands are in [KYVERNO.md](KYVERNO.md).

## Deployment to GKE

Requirements:
- `gcloud`, authenticated as an owner of the project
- `kubectl` and `helm`
- the [1Password CLI](https://developer.1password.com/docs/cli/) (`op`), once secrets exist

```bash
export PROJECT_ID=dev1-486920

# 1. GCP foundation + cluster (~10 min)
./gke-deploy.sh

# 2. Secrets -> Secret Manager (first time, and after rotating anything in 1Password)
op run --env-file=.env -- ./gke-secrets-seed.sh

# 3. Argo CD + everything else via GitOps
./gke-bootstrap.sh

# 4. Tools
./gke-port-forward.sh

# 5. The Envoy + OpenAPI 3.2 exercise: Argo CD does not deploy it; follow
#    "Deploying the exercise" below, then run the acceptance suite
```

**Before the first run:** push this repo to `github.com/miqui/gke-operator-lab` (public), branch
`main`. Argo CD syncs from there, not from your working copy. `gke-bootstrap.sh` checks that the
repo is readable and warns about unpushed commits.

**What each script does:**

- `gke-deploy.sh` is idempotent; every step skips what already exists. It:
  - checks your IAM permissions and enables the APIs;
  - creates the service accounts (`gke-dev-nodes`, `crossplane-gcp`, `external-secrets`) and the
    node SA's role;
  - creates the VPC, subnet, Cloud Router + NAT, the cluster and the webhook firewall rule;
  - binds Workload Identity for Crossplane and External Secrets.
- `gke-secrets-seed.sh` creates or updates the Secret Manager secrets listed in its `SECRETS`
  array, labelled `managed-by=gke-secrets-seed`. It adds a version only when the value changed, and
  grants `external-secrets` access to exactly those secrets.
- `gke-bootstrap.sh` runs a preflight (context, project guard, Secret Manager, public repo).
  It then Helm-installs Argo CD, applies the root Application and waits until every Application is
  Synced + Healthy. Safe to re-run.
- `gke-teardown.sh` works in dependency order:
  - It stops Argo CD reconciling, then deletes the cluster.
  - It then removes what a cluster deletion leaves behind: NEGs, and the unattached PD-CSI disks
    behind the PVCs.
  - Finally it removes the firewall rules, NAT, router, subnet and VPC.
  - Secrets and service accounts are kept unless you pass `--purge`. `--yes` skips the prompt.

**If your public IP changes**, the control plane stops answering (`kubectl` times out).
Re-authorize your IP with the command `gke-deploy.sh` prints at the end.

**Validate a teardown** (nothing should be listed):

```bash
gcloud container clusters list --project=$PROJECT_ID
gcloud compute disks list --project=$PROJECT_ID --filter='name~^pvc-'
gcloud compute networks list --project=$PROJECT_ID --filter=name=dev-vpc
```

Operational notes on GKE events that look alarming but aren't are in [GKE-OPS.md](GKE-OPS.md);
everyday `kubectl` is in [KUBECTL.md](KUBECTL.md).

### Headlamp

Headlamp can show everything in the cluster, so it gets the least access that is still useful.
`./gke-port-forward.sh headlamp` serves it at `http://localhost:4466`. Log in with a short-lived
token for its `view`-bound ServiceAccount (see [Stack URLs](#stack-urls)).

For anything that needs more access (editing, exec), use `kubectl` with your own Google identity.
Don't widen the `headlamp` binding: every Headlamp login would become an admin login.

### Polaris

`./gke-port-forward.sh polaris` serves it at `http://localhost:8082`. It shows an overall score
and, per workload, which built-in checks fail at `warning`/`danger`.
- GKE-managed namespaces are exempt (`config.exemptions` in `k8s/polaris/polaris-values.yaml`).
- Findings are advice. Anything that must be blocked belongs in a Kyverno policy.

## OpenAPI route operator (flights exercise)

An operator that keeps Envoy Gateway's routes in line with an OpenAPI 3.2 document: edit
`examples/flights/openapi.yaml`, run `./flights-publish.sh`, and the generated HTTPRoute changes.
Design and scope: [PLAN.md](PLAN.md), the capability specs in `openspec/specs/`, and the archived
change `openspec/changes/archive/2026-10-22-add-openapi-route-operator/`. The API,
status conditions, and safe-change rules: [the operator's README](src/openapi-route-operator/README.md).
Working on the operator (architecture diagrams, tests, releasing): [DEVELOPING.md](src/openapi-route-operator/DEVELOPING.md).

| Piece | Where |
| --- | --- |
| Operator (Go, Kubebuilder) | `src/openapi-route-operator/` |
| Envoy Gateway runtime | `k8s/envoy-gateway/` ([ENVOY.md](ENVOY.md)) |
| Gateway, path policy, fixtures | `k8s/flights/` |
| Flights contract | `examples/flights/openapi.yaml` |
| Access, troubleshooting, removal | [ENVOY.md](ENVOY.md) |

### Flights fixtures

`flight-query` and `flight-command` are **routing evidence, not a flight service**. Each answers
every method and path with `{"backend", "method", "path"}`, so a request shows which backend Envoy
chose and exactly what it forwarded. They have:

- **no persistence**: POST and PATCH change nothing; GET returns no flight data;
- **no authentication or authorization**;
- **no request or response validation** against the OpenAPI schemas.

The Gateway does not validate parameter formats either. `/flights/{flightId}` is a UUID in the
contract, but the route matches any single non-empty path segment, so `/flights/not-a-uuid`
reaches a backend. Validating it would be the backend's job. Nor does the route represent QUERY or
custom (`additionalOperations`) methods: the operator rejects documents that use them.

### Deploying the exercise

On a cluster from `gke-deploy.sh`, in this order (each step's dependencies already exist):

```bash
K=gke_dev1-486920_us-central1-a_dev-cluster
./k8s/envoy-gateway/install.sh                      # CRDs (preflight first), controller, GatewayClass
kubectl --context $K apply -k k8s/flights           # namespace, Gateway, path policy, fixtures
(cd src/openapi-route-operator && make docker-push) # only when the operator changed; then pin it:
#   cd k8s/operator && ../../src/openapi-route-operator/bin/kustomize edit set image \
#     controller=$(cat ../../src/openapi-route-operator/bin/image)
kubectl --context $K apply -k k8s/operator          # CRD, namespaced RBAC, Deployment
./flights-publish.sh                                # the document
kubectl --context $K apply -k k8s/flights/api       # the OpenAPIRoute
kubectl --context $K -n flights-lab wait openapiroute/flights --for=condition=Ready --timeout=2m
```

### Acceptance suite (GKE)

The proof that spec edits change live traffic. It runs against a **named** context only, needs
the exercise deployed and Ready, and puts the baseline document and OpenAPIRoute back when done:

```bash
cd src/openapi-route-operator
make test-e2e KUBE_CONTEXT=gke_dev1-486920_us-central1-a_dev-cluster
```

It republishes variants of `examples/flights/openapi.yaml` through the same ConfigMap as
`flights-publish.sh`, changes bindings, edits/deletes the generated route, scales the operator to
zero and back, and sends real requests through its own loopback port-forward (random port, `Host:
flights.example.test`). Every wait is bounded (90 s; 3 min after a restart). Coverage:

| Area | Specs |
| --- | --- |
| Spec edits → traffic | method routing; templated routes; add an operation (no operator restart); change a binding; remove an operation (→ 404); invalid and unsupported edits keep the old route; description-only edit leaves the route untouched; literal path beats template |
| Outside the contract | wrong Host, undeclared methods, trailing slash, `//`, extra segments, `/flights-extra` → gateway 404; `%2F` → 400 |
| Ownership and drift | hand edit reverted; deleted route recreated; restart with drift made while down; unrelated route untouched; name collision reported, not taken over |
| Readiness | missing bound Service → not Ready; fixed → Ready |
| Removal | empty API → route deleted, no catch-all; OpenAPIRoute deleted → route garbage-collected |

**Reading a failure.** The Ginkgo output names the spec and the request or condition that did not
converge (e.g. `GET /flights/…: expected "200 from flight-command" got "404 from the gateway"`).
For every failed spec the suite writes `test/e2e/artifacts/<spec>/`: the OpenAPIRoute and
HTTPRoutes with their status, the Gateway, the published ConfigMap, namespace events, and the
operator and Envoy Gateway logs. Start with `openapiroute.yaml`: `SourceValid` says whether the
document compiled, `RouteApplied` whether the route was written, and `Ready` whether Envoy
accepted it. A missing `KUBE_CONTEXT` fails the run; it never counts as a pass.
`E2E_SELFTEST_FAIL=1` adds one deliberately failing spec to check the diagnostics path.

### Verified deployment (2026-10-22)

The full sequence under "Deploying the exercise" was run on the dev cluster, followed by two
consecutive `make test-e2e` runs: 24/24 specs passed each time (~50 s).

| Component | Version |
| --- | --- |
| GKE | `dev-cluster`, v1.35.6-gke.1250001 (stable channel), GKE Gateway API disabled |
| Envoy Gateway | v1.9.2 (Envoy distroless-v1.39.x) |
| Gateway API CRDs | v1.6.1, standard channel |
| Operator image | `us-central1-docker.pkg.dev/dev1-486920/operator-lab/openapi-route-operator@sha256:e3ed1da8e806046a83b7731c1fed4db263bb4be85f26eb804d5a5f61558d22d3` (built from `e89752135ff2`) |
| Operator dependencies | controller-runtime v0.25.0, Gateway API Go module v1.6.1, libopenapi v0.41.6 |
| Fixtures | `nginx:1.31.6-alpine` |
