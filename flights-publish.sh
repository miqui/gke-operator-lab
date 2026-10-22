#!/usr/bin/env bash
#
# flights-publish.sh — publish the flights OpenAPI document to the cluster, the APIOps step that
# makes the OpenAPI route operator regenerate the Gateway's routes.
#
# The document goes into a ConfigMap whose name never changes, so the OpenAPIRoute keeps pointing
# at it and every publish is just new data. Server-side apply (no last-applied annotation, which
# would store a second copy of the document and halve the usable ConfigMap size).
#
# After publishing, compare the printed digest with the OpenAPIRoute's status.sourceDigest to see
# that the operator read this version, and check SourceValid/Ready for the outcome.
#
# Usage:
#   ./flights-publish.sh                              # examples/flights/openapi.yaml
#   SPEC_FILE=path/to/openapi.yaml ./flights-publish.sh
#
# Overridable env vars: KUBE_CONTEXT, NAMESPACE, CONFIGMAP, SPEC_FILE
set -euo pipefail
trap 'echo "ERROR: failed at line $LINENO (exit $?)" >&2' ERR
cd "$(dirname "$0")"

KUBE_CONTEXT="${KUBE_CONTEXT:-gke_dev1-486920_us-central1-a_dev-cluster}"
NAMESPACE="${NAMESPACE:-flights-lab}"
CONFIGMAP="${CONFIGMAP:-flights-openapi}"
SPEC_FILE="${SPEC_FILE:-examples/flights/openapi.yaml}"

command -v kubectl >/dev/null 2>&1 || { echo "kubectl not found" >&2; exit 1; }
[[ -f "$SPEC_FILE" ]] || { echo "ERROR: $SPEC_FILE not found" >&2; exit 1; }

kubectl --context "$KUBE_CONTEXT" create configmap "$CONFIGMAP" --namespace "$NAMESPACE" \
  --from-file=openapi.yaml="$SPEC_FILE" --dry-run=client -o yaml |
  kubectl --context "$KUBE_CONTEXT" apply --server-side --field-manager=flights-publish -f -

echo "published $SPEC_FILE -> $NAMESPACE/$CONFIGMAP (sha256:$(shasum -a 256 "$SPEC_FILE" | cut -d' ' -f1))"
