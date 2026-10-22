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

package controller

import (
	metav1ac "k8s.io/client-go/applyconfigurations/meta/v1"
	"k8s.io/utils/ptr"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
	gatewayv1ac "sigs.k8s.io/gateway-api/applyconfiguration/apis/v1"

	routingv1alpha1 "github.com/miqui/gke-operator-lab/src/openapi-route-operator/api/v1alpha1"
	"github.com/miqui/gke-operator-lab/src/openapi-route-operator/internal/compiler"
)

const (
	// fieldManager owns the generated route's fields under server-side apply.
	fieldManager = "openapi-route-operator"
	// planDigestAnnotation records which plan a route was generated from.
	planDigestAnnotation = "routing.lab.dev/plan-digest"
	managedByLabel       = "app.kubernetes.io/managed-by"
)

// desiredRoute is the complete HTTPRoute for a plan, as an apply configuration: only the fields
// set here are owned by the operator, so Envoy Gateway's status and anyone else's labels or
// annotations are left alone.
func desiredRoute(owner *routingv1alpha1.OpenAPIRoute, plan *compiler.Plan) *gatewayv1ac.HTTPRouteApplyConfiguration {
	gvk := routingv1alpha1.GroupVersion.WithKind("OpenAPIRoute")
	spec := gatewayv1ac.HTTPRouteSpec().WithHostnames(owner.Spec.Hostnames...)
	for _, p := range owner.Spec.ParentRefs {
		spec.WithParentRefs(parentRef(p))
	}
	for _, rule := range plan.Rules {
		r := gatewayv1ac.HTTPRouteRule().WithBackendRefs(
			gatewayv1ac.HTTPBackendRef().
				WithName(gatewayv1.ObjectName(rule.Backend.Name)).
				WithPort(rule.Backend.Port))
		for _, m := range rule.Matches {
			r.WithMatches(gatewayv1ac.HTTPRouteMatch().
				WithMethod(gatewayv1.HTTPMethod(m.Method)).
				WithPath(gatewayv1ac.HTTPPathMatch().
					WithType(gatewayv1.PathMatchType(m.PathType)).
					WithValue(m.Path)))
		}
		spec.WithRules(r)
	}

	return gatewayv1ac.HTTPRoute(owner.Name, owner.Namespace).
		WithLabels(map[string]string{managedByLabel: fieldManager}).
		WithAnnotations(map[string]string{planDigestAnnotation: plan.Digest}).
		WithOwnerReferences(metav1ac.OwnerReference().
			WithAPIVersion(gvk.GroupVersion().String()).
			WithKind(gvk.Kind).
			WithName(owner.Name).
			WithUID(owner.UID).
			WithController(true).
			WithBlockOwnerDeletion(true)).
		WithSpec(spec)
}

func parentRef(p gatewayv1.ParentReference) *gatewayv1ac.ParentReferenceApplyConfiguration {
	ref := gatewayv1ac.ParentReference().WithName(p.Name)
	if p.Group != nil {
		ref.WithGroup(*p.Group)
	}
	if p.Kind != nil {
		ref.WithKind(*p.Kind)
	}
	if p.Namespace != nil {
		ref.WithNamespace(*p.Namespace)
	}
	if p.SectionName != nil {
		ref.WithSectionName(*p.SectionName)
	}
	if p.Port != nil {
		ref.WithPort(*p.Port)
	}
	return ref
}

// compilerInput maps the OpenAPIRoute's bindings onto the compiler's input.
func compilerInput(owner *routingv1alpha1.OpenAPIRoute, document []byte) compiler.Input {
	bindings := make(map[string]compiler.Backend, len(owner.Spec.OperationBindings))
	for _, b := range owner.Spec.OperationBindings {
		bindings[b.OperationID] = backend(b.BackendRef)
	}
	return compiler.Input{
		Document:       document,
		BasePath:       owner.Spec.BasePath,
		DefaultBackend: backend(owner.Spec.DefaultBackendRef),
		Bindings:       bindings,
	}
}

func backend(b routingv1alpha1.BackendRef) compiler.Backend {
	return compiler.Backend{Name: b.Name, Port: b.Port}
}

// gatewayKey identifies a parentRef's Gateway, defaulting the namespace to the route's.
func gatewayKey(owner *routingv1alpha1.OpenAPIRoute, p gatewayv1.ParentReference) (namespace, name string, isGateway bool) {
	group := ptr.Deref(p.Group, gatewayv1.GroupName)
	kind := ptr.Deref(p.Kind, "Gateway")
	ns := owner.Namespace
	if p.Namespace != nil {
		ns = string(*p.Namespace)
	}
	return ns, string(p.Name), group == gatewayv1.GroupName && kind == "Gateway"
}
