# Helm charts — how they're installed and changed

Every third-party component is an upstream Helm chart **rendered by Argo CD**, not installed with
`helm install`. Each Application in `k8s/argocd/apps/` pairs the chart (pinned `targetRevision`)
with a values file from this repo (the `$values` multi-source pattern), so changing a value or
bumping a version is a merged commit. There are no Helm releases in the cluster for these - `helm
list -A` shows only `argocd`, the one chart `gke-bootstrap.sh` installs with Helm before Argo CD
exists to take it over.

| Component | Chart | Version | Values | Application |
| --- | --- | --- | --- | --- |
| Argo CD | `argo/argo-cd` | 10.9.2 (v3.5.3) | `k8s/argocd/argocd-values.yaml` | `argocd` |
| External Secrets | `external-secrets/external-secrets` | 2.11.0 | `k8s/external-secrets/values.yaml` | `external-secrets` |
| Crossplane | `crossplane-stable/crossplane` | 2.4.2 | `k8s/crossplane/values.yaml` | `crossplane` |
| Kyverno | `kyverno/kyverno` | 3.9.1 (v1.19.1) | `k8s/kyverno/kyverno-values.yaml` | `kyverno` |
| Headlamp | `headlamp/headlamp` | 0.45.0 | `k8s/headlamp/headlamp-values.yaml` | `headlamp` |
| Polaris | `fairwinds-stable/polaris` | 6.0.1 | `k8s/polaris/polaris-values.yaml` | `polaris` |
| Trivy Operator | `aqua/trivy-operator` | 0.36.0 | `k8s/trivy-operator/trivy-operator-values.yaml` | `trivy-operator` |

Repositories: `argo` https://argoproj.github.io/argo-helm, `external-secrets`
https://charts.external-secrets.io, `crossplane-stable` https://charts.crossplane.io/stable,
`kyverno` https://kyverno.github.io/kyverno/, `headlamp` https://kubernetes-sigs.github.io/headlamp/,
`fairwinds-stable` https://charts.fairwinds.com/stable, `aqua` https://aquasecurity.github.io/helm-charts/.

## Changing values or versions

1. Edit the values file (or `targetRevision` in the Application) on a branch.
2. Render locally to check it, with the same chart version:
   ```bash
   helm repo add headlamp https://kubernetes-sigs.github.io/headlamp/ && helm repo update headlamp
   helm template headlamp headlamp/headlamp --version 0.45.0 \
     -n headlamp -f k8s/headlamp/headlamp-values.yaml | less
   helm show values headlamp/headlamp --version 0.45.0   # what can be set
   ```
3. Merge. Argo CD re-renders and applies it; charts that hash their config into the pod template
   (a `checksum/config` pod annotation) roll their pods on their own.

Bumping Argo CD's own chart: change `targetRevision` in `k8s/argocd/apps/argocd.yaml` **and**
`ARGOCD_CHART_VERSION` in `gke-bootstrap.sh`, so fresh clusters start on the same version.

## Envoy Gateway (operator exercise, installed with `helm`, not Argo CD)

The OpenAPI route operator's Gateway runtime is installed from this branch with
`k8s/envoy-gateway/install.sh`, outside Argo CD: the platform root watches another repository's
`main`, and one owner per release is the rule (see PLAN.md §7D). So, unlike the table above,
`helm list -n envoy-gateway-system` shows a real release (`eg`).

| Component | Chart | Version | Values | Release |
| --- | --- | --- | --- | --- |
| Gateway API + Envoy Gateway CRDs | `oci://docker.io/envoyproxy/gateway-crds-helm` | v1.9.2 | `k8s/envoy-gateway/crds-values.yaml` | none: `helm template` + server-side apply, field manager `eg-crds` |
| Envoy Gateway controller | `oci://docker.io/envoyproxy/gateway-helm` | v1.9.2 (installed with `--skip-crds`) | `k8s/envoy-gateway/values.yaml` | `eg` |

Why these pins (checked 2026-10-22 against the [compatibility
matrix](https://gateway.envoyproxy.io/news/releases/matrix/)): Envoy Gateway v1.9 supports
Kubernetes v1.33-v1.36 and is built against **Gateway API v1.6.1**; the dev cluster runs
v1.35.6 (GKE-OPS.md "Cluster facts"). The operator's `go.mod` pins the same Gateway API v1.6.1,
and its envtest uses the matching standard-channel CRDs, so the operator, its tests, and the
cluster agree on one API.

- **Standard channel.** The CRD chart defaults to the experimental channel and to installing no
  Gateway API CRDs at all; `crds-values.yaml` sets `gatewayAPI.enabled: true, channel: standard`.
  The operator only emits standard HTTPRoute fields.
- **The lab owns the Gateway API CRDs.** GKE's managed Gateway API is disabled at cluster creation
  (`--gateway-api=disabled`); `install.sh` refuses to run if any Gateway API CRD exists that the
  `eg-crds` field manager does not own. Deleting these CRDs deletes every Gateway API object in
  the cluster, so it is a separate, explicit step (KUBECTL.md "Removing the exercise").
- **Why not a Helm release for the CRDs.** The v1.9.2 bundle makes Helm's release Secret exceed
  Kubernetes' 1 MiB object limit (`Secret "sh.helm.release.v1.eg-crds.v1" is invalid: data: Too
  long`), which is why Envoy Gateway documents `helm template | kubectl apply --server-side`.
  `install.sh` pulls the chart first and renders the local copy: Helm v4 prints `Pulled:` and
  `Digest:` to stdout when rendering straight from a registry.
- **Bumping.** Bump Envoy Gateway, the Gateway API version in the operator's `go.mod`, and the
  matrix check together; then `make manifests` (re-copies the CRDs for envtest) and `make test`.
