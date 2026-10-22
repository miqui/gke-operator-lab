# GKE Operations Notes

Short explanations of cluster events that look alarming but aren't. The cluster is zonal,
`e2-standard-2` workers, node autoscaling 3..5 (`gke-deploy.sh`).

## Cluster facts

Created 2026-10-22 with `PROJECT_ID=dev1-486920 ./gke-deploy.sh`:

| | |
| --- | --- |
| Cluster | `dev-cluster`, zonal `us-central1-a`, project `dev1-486920` |
| Kubernetes | `v1.35.6-gke.1250001`, release channel `STABLE` (auto-upgrades within the channel) |
| GKE Gateway API | `CHANNEL_DISABLED`: GKE installs no Gateway API CRDs; the Envoy runtime owns them |
| Image registry | Artifact Registry `us-central1-docker.pkg.dev/dev1-486920/operator-lab`; `gke-dev-nodes` has `roles/artifactregistry.reader` on that repository only |
| Control plane access | Authorized network is the operator's public IP at creation time |

Check them with the "Foundation checks" in [KUBECTL.md](KUBECTL.md). Pins that depend on the
Kubernetes version (Envoy Gateway, Gateway API, envtest) are recorded in [ENVOY.md](ENVOY.md).

If `kubectl` times out after your public IP changes, re-authorize the new one (this is the
command `gke-deploy.sh` prints):

```bash
gcloud container clusters update dev-cluster --project=dev1-486920 --zone=us-central1-a \
  --enable-master-authorized-networks --master-authorized-networks="$(curl -4 -s ifconfig.me)/32"
```

## "Cluster Autoscaler skipped scaling up default-pool" for `node-collector-*`

Seen as a `NotTriggerScaleUp` note, e.g.:

> The pod node-collector-6cdc7f778f-265nh has a nodeSelector or nodeAffinity block configured...
> Because the default-pool node pool does not carry the matching labels... the Cluster Autoscaler
> skipped scaling up this pool.

**Benign - no action needed.**

- `node-collector` is Trivy Operator's per-node scan job ([TRIVY.md](TRIVY.md)). The operator pins
  each one to a single node with a `kubernetes.io/hostname` nodeSelector.
- A pod pinned to one node cannot run on any other, and a new node has a different hostname, so
  adding nodes never helps. The autoscaler correctly reports this and skips the scale-up.
- It typically appears when the target node is being removed. The first scan pass scans every
  workload at once and can briefly add a node; once idle, the autoscaler scales it back down
  (`ScaleDown ... marked the node as toBeDeleted/unschedulable`). A collector job pinned to that
  node becomes unschedulable and is deleted with it.
- The `NodeRegistrationCheckerDidNotRunChecks` warning on such a node is noise; its message says
  the node was ready and registered.

### Verify

```bash
kubectl get nodes                                             # one node Ready,SchedulingDisabled = scale-down in progress
kubectl get events -A --sort-by=.lastTimestamp | grep -E "node-collector|ScaleDown"
kubectl get pods -A --field-selector=status.phase=Pending     # expect none
kubectl get pods -n trivy-system                              # operator + trivy-server-0 Running
```

If no `node-collector-*` job shows up after the next scan cycle, check
`kubectl logs deploy/trivy-operator -n trivy-system`.
