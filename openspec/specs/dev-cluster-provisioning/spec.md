# dev-cluster-provisioning Specification

## Purpose

Provision the GKE dev cluster foundation for this project with the repository's own script, so the cluster comes up ready for a lab-owned Gateway runtime and an operator image from a private registry.

## Requirements

### Requirement: Cluster is created in the designated project and location
Provisioning SHALL create, or reuse if it already exists, the zonal cluster `dev-cluster` in `us-central1-a` within project `dev1-486920`, using `gke-deploy.sh` with its existing network, node, and access defaults. It SHALL NOT change the user's default gcloud project.

#### Scenario: First run creates the cluster
- **WHEN** `PROJECT_ID=dev1-486920 ./gke-deploy.sh` runs and no `dev-cluster` exists
- **THEN** the cluster is created with private nodes and a control plane restricted to the operator's IP
- **AND** a kubeconfig context for that cluster is fetched

#### Scenario: Re-run reuses existing resources
- **WHEN** the script runs again after a successful run
- **THEN** each existing resource is reported as already existing and none is recreated

### Requirement: Existence checks distinguish absence from failure
Provisioning SHALL enter a create step only when the resource lookup reports NotFound. Any other lookup failure SHALL stop provisioning and report the gcloud error.

#### Scenario: Lookup is denied
- **WHEN** a resource lookup fails because of a permission, disabled API, or network error
- **THEN** provisioning exits non-zero with the gcloud error and does not attempt to create that resource

### Requirement: GKE does not manage Gateway API definitions
The cluster SHALL be created with GKE's managed Gateway API disabled, so that GKE neither installs nor reverts Gateway API CRDs.

#### Scenario: Fresh cluster has no provider-managed Gateway CRDs
- **WHEN** the cluster has been created and before the Envoy runtime is installed
- **THEN** no Gateway API CRDs carrying GKE-managed ownership labels are present

### Requirement: Nodes can pull the operator image from a project registry
Provisioning SHALL enable the Artifact Registry API and ensure that a Docker repository exists in `us-central1` for the operator image. The node service account SHALL be able to read that repository only, with no project-wide registry role.

#### Scenario: Registry access is scoped
- **WHEN** IAM bindings are inspected after provisioning
- **THEN** `gke-dev-nodes` holds `roles/artifactregistry.reader` on the operator repository
- **AND** it holds no project-level Artifact Registry role

#### Scenario: Operator image pulls without credentials
- **WHEN** a Pod references an image in the operator repository
- **THEN** the image pulls successfully without an image pull Secret
