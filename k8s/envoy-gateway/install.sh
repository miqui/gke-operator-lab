#!/usr/bin/env bash
#
# install.sh — install the Envoy Gateway runtime for the OpenAPI route operator exercise: the
# Gateway API + Envoy Gateway CRDs, the controller (Helm release eg), and the ClusterIP
# GatewayClass `flights-envoy`. Pins and rationale: HELM.md "Envoy Gateway".
#
# The CRDs are rendered with `helm template` and server-side applied under field manager eg-crds,
# as Envoy Gateway documents: the bundle is too large for a Helm release Secret (1 MiB limit).
#
# The lab owns the Gateway API CRDs. Before installing, a preflight refuses to continue if any
# gateway.networking.k8s.io CRD exists that the eg-crds field manager does not own (for example
# GKE's managed Gateway API): those are cluster-wide, and overwriting someone else's would break
# them.
#
# Usage:
#   ./k8s/envoy-gateway/install.sh                    # preflight, install, verify
#   ./k8s/envoy-gateway/install.sh --preflight-only   # just the CRD ownership check
#
# Overridable env vars: KUBE_CONTEXT (default: the dev cluster's context)
set -euo pipefail
trap 'echo "ERROR: failed at line $LINENO (exit $?)" >&2' ERR
cd "$(dirname "$0")"

KUBE_CONTEXT="${KUBE_CONTEXT:-gke_dev1-486920_us-central1-a_dev-cluster}"
EG_VERSION="v1.9.2"
EG_NAMESPACE="envoy-gateway-system"
CRDS_MANAGER="eg-crds"   # field manager that owns the CRDs
EG_RELEASE="eg"

log() { printf '\n\033[1;34m==> %s\033[0m\n' "$*"; }
kc() { kubectl --context "$KUBE_CONTEXT" "$@"; }
hc() { helm --kube-context "$KUBE_CONTEXT" "$@"; }

for tool in kubectl helm; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$tool not found" >&2; exit 1; }
done

# ---- Preflight: who owns the Gateway API CRDs? ----------------------------------------------
# Lists "<crd> <field managers...>" for every CRD; a Gateway API CRD without our field manager is
# foreign.
preflight() {
  log "Preflight: Gateway API CRD ownership ($KUBE_CONTEXT)"
  local foreign
  foreign=$(kc get crd -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.metadata.managedFields[*].manager}{"\n"}{end}' |
    awk -v mgr="$CRDS_MANAGER" '$1 ~ /\.gateway\.networking\.k8s\.io$/ {
      ours = 0; for (i = 2; i <= NF; i++) if ($i == mgr) ours = 1
      if (!ours) print "    " $1 }')
  if [[ -n "$foreign" ]]; then
    cat >&2 <<EOF
ERROR: these Gateway API CRDs exist and are not owned by field manager $CRDS_MANAGER:
$foreign

They may be GKE-managed (check: gcloud container clusters describe ... \\
  --format='value(networkConfig.gatewayApiConfig.channel)' should be CHANNEL_DISABLED) or
installed by something else. Not overwriting them. See HELM.md "Envoy Gateway".
EOF
    exit 1
  fi
  echo "    no foreign Gateway API CRDs"
}

preflight
[[ "${1:-}" == "--preflight-only" ]] && exit 0

# ---- CRDs, then controller ---------------------------------------------------------------------
log "Installing CRDs (gateway-crds-helm $EG_VERSION, server-side apply as $CRDS_MANAGER)"
# Pull first, then render the local copy: Helm v4 prints "Pulled:/Digest:" to stdout when
# rendering straight from a registry, which would corrupt the manifest stream.
CHART_DIR=$(mktemp -d)
trap 'rm -rf "$CHART_DIR"' EXIT
helm pull oci://docker.io/envoyproxy/gateway-crds-helm --version "$EG_VERSION" --destination "$CHART_DIR" >/dev/null
hc template eg-crds "$CHART_DIR/gateway-crds-helm-$EG_VERSION.tgz" --values crds-values.yaml |
  kc apply --server-side --field-manager="$CRDS_MANAGER" -f -
kc wait crd --all --for=condition=Established --timeout=2m >/dev/null

log "Installing controller: $EG_RELEASE (gateway-helm $EG_VERSION, --skip-crds)"
hc upgrade --install "$EG_RELEASE" oci://docker.io/envoyproxy/gateway-helm \
  --version "$EG_VERSION" --namespace "$EG_NAMESPACE" --create-namespace \
  --skip-crds --values values.yaml --wait
kc -n "$EG_NAMESPACE" wait deployment/envoy-gateway --for=condition=Available --timeout=5m

# ---- GatewayClass ------------------------------------------------------------------------------
log "Applying GatewayClass flights-envoy (ClusterIP EnvoyProxy)"
kc apply -f gatewayclass.yaml
kc wait gatewayclass/flights-envoy --for=condition=Accepted --timeout=2m

# ---- Verify ------------------------------------------------------------------------------------
log "Verifying"
kc get crd httproutes.gateway.networking.k8s.io \
  -o jsonpath='    Gateway API {.metadata.annotations.gateway\.networking\.k8s\.io/bundle-version} ({.metadata.annotations.gateway\.networking\.k8s\.io/channel} channel){"\n"}'
kc -n "$EG_NAMESPACE" get deployment envoy-gateway \
  -o jsonpath='    controller image {.spec.template.spec.containers[0].image}{"\n"}'
kc get gatewayclass flights-envoy

cat <<EOF

$(printf '\033[1;32mDone.\033[0m') Envoy Gateway $EG_VERSION is running in $EG_NAMESPACE.
  Next: kubectl --context $KUBE_CONTEXT apply -k k8s/flights   (Gateway, fixtures)
EOF
