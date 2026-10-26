/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

// OpenAPIRouteSpec binds an OpenAPI 3.2 document to a Gateway: where the document is, which
// Gateway listener serves it, under which hostnames and path prefix, and which Service handles
// each operation. The document describes the API; this spec decides how it is exposed.
type OpenAPIRouteSpec struct {
	// documentRef points at the ConfigMap entry holding the OpenAPI 3.2 document (YAML or JSON).
	// +required
	DocumentRef DocumentRef `json:"documentRef"`

	// parentRefs are the Gateways (and optionally listeners) the generated HTTPRoute attaches to.
	// +required
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=32
	ParentRefs []gatewayv1.ParentReference `json:"parentRefs"`

	// hostnames the generated HTTPRoute matches. Empty means the listener's hostnames.
	// +optional
	// +kubebuilder:validation:MaxItems=16
	Hostnames []gatewayv1.Hostname `json:"hostnames,omitempty"`

	// basePath is prefixed to every path in the document, e.g. /api. Empty for none. The
	// document's servers are informational and never change where the API is exposed.
	// +optional
	// +kubebuilder:validation:MaxLength=512
	// +kubebuilder:validation:Pattern=`^(/[-A-Za-z0-9._~!$&'()*+,;=:@]+)*$`
	BasePath string `json:"basePath,omitempty"`

	// defaultBackendRef handles every operation without an operationBinding.
	// +required
	DefaultBackendRef BackendRef `json:"defaultBackendRef"`

	// operationBindings send specific operations, by operationId, to another Service.
	// +optional
	// +listType=map
	// +listMapKey=operationId
	// +kubebuilder:validation:MaxItems=128
	OperationBindings []OperationBinding `json:"operationBindings,omitempty"`
}

// DocumentRef is a key in a ConfigMap in the OpenAPIRoute's namespace.
type DocumentRef struct {
	// name of the ConfigMap.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Name string `json:"name"`

	// key in the ConfigMap's data holding the document.
	// +optional
	// +kubebuilder:default="openapi.yaml"
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	Key string `json:"key,omitempty"`
}

// BackendRef is a Service in the OpenAPIRoute's namespace.
type BackendRef struct {
	// name of the Service.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	Name string `json:"name"`

	// port of the Service.
	// +required
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port"`
}

// OperationBinding routes one operation to a backend other than the default.
type OperationBinding struct {
	// operationId of the operation in the document.
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=256
	OperationID string `json:"operationId"`

	// backendRef handles this operation.
	// +required
	BackendRef BackendRef `json:"backendRef"`
}

// Condition types reported in OpenAPIRouteStatus.conditions.
const (
	// ConditionSourceValid: the document compiled into a routing plan.
	ConditionSourceValid = "SourceValid"
	// ConditionRouteApplied: the generated HTTPRoute matches the current plan.
	ConditionRouteApplied = "RouteApplied"
	// ConditionDependenciesResolved: every referenced Service and Gateway exists.
	ConditionDependenciesResolved = "DependenciesResolved"
	// ConditionReady: the route is accepted with resolved refs for its current generation, and
	// its Gateway is programmed.
	ConditionReady = "Ready"
)

// OpenAPIRouteStatus is what the operator last observed and did.
type OpenAPIRouteStatus struct {
	// observedGeneration is the OpenAPIRoute generation these conditions describe. ConfigMap
	// edits do not change it; compare sourceDigest for those.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// sourceDigest is the sha256 of the last document read, valid or not.
	// +optional
	SourceDigest string `json:"sourceDigest,omitempty"`

	// planDigest is the digest of the routing plan currently applied. It lags sourceDigest's
	// document while that document fails to compile, and is unchanged by non-routing edits.
	// +optional
	PlanDigest string `json:"planDigest,omitempty"`

	// routeName is the generated HTTPRoute, empty when the plan routes nothing.
	// +optional
	RouteName string `json:"routeName,omitempty"`

	// notProjected lists operations deliberately not routed, such as webhooks and callbacks.
	// +optional
	// +listType=atomic
	NotProjected []string `json:"notProjected,omitempty"`

	// conditions: SourceValid, RouteApplied, DependenciesResolved, Ready.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Reason",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].reason`
// +kubebuilder:printcolumn:name="Route",type=string,JSONPath=`.status.routeName`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// OpenAPIRoute projects an OpenAPI 3.2 document onto a Gateway as one generated HTTPRoute.
type OpenAPIRoute struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of OpenAPIRoute
	// +required
	Spec OpenAPIRouteSpec `json:"spec"`

	// status defines the observed state of OpenAPIRoute
	// +optional
	Status OpenAPIRouteStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// OpenAPIRouteList contains a list of OpenAPIRoute
type OpenAPIRouteList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []OpenAPIRoute `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &OpenAPIRoute{}, &OpenAPIRouteList{})
		return nil
	})
}
