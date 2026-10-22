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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	routingv1alpha1 "github.com/miqui/gke-operator-lab/src/openapi-route-operator/api/v1alpha1"
	"github.com/miqui/gke-operator-lab/src/openapi-route-operator/internal/compiler"
)

// Field indexes on OpenAPIRoute, used to map ConfigMap, Service, and Gateway events back to the
// OpenAPIRoutes that reference them.
const (
	indexDocument = ".spec.documentRef.name"
	indexBackends = ".spec.backendServices"
	indexGateways = ".spec.parentGateways" // "<namespace>/<name>"
)

// Condition reasons. Compilation failures use the compiler's Kind as the reason.
const (
	ReasonCompiled              = "Compiled"
	ReasonDocumentNotFound      = "DocumentNotFound"
	ReasonDocumentKeyNotFound   = "DocumentKeyNotFound"
	ReasonApplied               = "Applied"
	ReasonNoOperations          = "NoOperations"
	ReasonNameConflict          = "NameConflict"
	ReasonApplyFailed           = "ApplyFailed"
	ReasonNotApplied            = "NotApplied"
	ReasonResolved              = "Resolved"
	ReasonBackendNotFound       = "BackendNotFound"
	ReasonGatewayNotFound       = "GatewayNotFound"
	ReasonReady                 = "Ready"
	ReasonSourceInvalid         = "SourceInvalid"
	ReasonDependenciesMissing   = "DependenciesMissing"
	ReasonPending               = "Pending"
	ReasonRouteNotAccepted      = "RouteNotAccepted"
	ReasonRefsNotResolved       = "RefsNotResolved"
	ReasonGatewayNotProgrammed  = "GatewayNotProgrammed"
	maxConditionMessageBytes    = 4096
	defaultDocumentKey          = "openapi.yaml"
	gatewayProgrammedCondition  = string(gatewayv1.GatewayConditionProgrammed)
	routeAcceptedCondition      = string(gatewayv1.RouteConditionAccepted)
	routeResolvedRefsCondition  = string(gatewayv1.RouteConditionResolvedRefs)
	eventActionCompile          = "Compile"
	eventActionApply            = "Apply"
	eventActionDelete           = "Delete"
	conditionMessageTruncSuffix = " …(truncated)"

	// pendingRequeue bounds how long Ready can lag the Gateway controller. Its status writes on
	// the route and Gateway normally trigger a reconcile through the watches, but on GKE a
	// status write landing in the same instant as the operator's own read was occasionally
	// never followed by another reconcile, leaving Ready=Pending indefinitely. Waiting on
	// another controller should not depend on a single event.
	pendingRequeue = 5 * time.Second
)

// OpenAPIRouteReconciler keeps one generated HTTPRoute in line with an OpenAPIRoute's document.
type OpenAPIRouteReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder
	// Limits defaults to compiler.DefaultLimits when zero.
	Limits compiler.Limits
}

// +kubebuilder:rbac:groups=routing.lab.dev,namespace=flights-lab,resources=openapiroutes,verbs=get;list;watch
// +kubebuilder:rbac:groups=routing.lab.dev,namespace=flights-lab,resources=openapiroutes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=routing.lab.dev,namespace=flights-lab,resources=openapiroutes/finalizers,verbs=update
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,namespace=flights-lab,resources=httproutes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=gateway.networking.k8s.io,namespace=flights-lab,resources=gateways,verbs=get;list;watch
// +kubebuilder:rbac:groups="",namespace=flights-lab,resources=configmaps;services,verbs=get;list;watch
// +kubebuilder:rbac:groups=events.k8s.io,namespace=flights-lab,resources=events,verbs=create;patch

// Reconcile compiles the referenced document, applies the owned HTTPRoute when the plan is
// valid, and reports what happened. Invalid input never changes an existing route.
func (r *OpenAPIRouteReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var owner routingv1alpha1.OpenAPIRoute
	if err := r.Get(ctx, req.NamespacedName, &owner); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	before := owner.Status.DeepCopy()
	st := &owner.Status
	st.ObservedGeneration = owner.Generation

	plan, err := r.compile(ctx, &owner)
	if err != nil {
		return ctrl.Result{}, err
	}

	var existing gatewayv1.HTTPRoute
	found := true
	if err := r.Get(ctx, types.NamespacedName{Namespace: owner.Namespace, Name: owner.Name}, &existing); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		found = false
	}
	owned := found && metav1.IsControlledBy(&existing, &owner)

	// route is the generated route whose Gateway status decides readiness, if there is one.
	var route *gatewayv1.HTTPRoute
	var applyErr error
	switch {
	case found && !owned:
		r.setCondition(&owner, routingv1alpha1.ConditionRouteApplied, metav1.ConditionFalse, ReasonNameConflict,
			"HTTPRoute %s already exists and is not owned by this OpenAPIRoute; it was not modified", owner.Name)
	case plan == nil:
		if owned {
			route = &existing
			r.setCondition(&owner, routingv1alpha1.ConditionRouteApplied, metav1.ConditionFalse, ReasonNotApplied,
				"source is not valid; HTTPRoute %s still serves plan %s", owner.Name, existing.Annotations[planDigestAnnotation])
		} else {
			st.RouteName = ""
			r.setCondition(&owner, routingv1alpha1.ConditionRouteApplied, metav1.ConditionFalse, ReasonNotApplied,
				"source is not valid; no route was created")
		}
	case plan.Empty():
		if owned {
			if err := r.Delete(ctx, &existing); client.IgnoreNotFound(err) != nil {
				return ctrl.Result{}, err
			}
			log.Info("deleted route: plan has no routable operations", "route", owner.Name)
		}
		st.RouteName, st.PlanDigest = "", plan.Digest
		r.setCondition(&owner, routingv1alpha1.ConditionRouteApplied, metav1.ConditionTrue, ReasonNoOperations,
			"the document has no routable operations, so there is no route")
	default:
		// Server-side apply is idempotent: an unchanged plan is a no-op on the server, with no
		// new resourceVersion or generation. Forcing reclaims fields edited by hand (drift).
		applyErr = r.Apply(ctx, desiredRoute(&owner, plan), client.FieldOwner(fieldManager), client.ForceOwnership)
		if applyErr != nil {
			r.setCondition(&owner, routingv1alpha1.ConditionRouteApplied, metav1.ConditionFalse, ReasonApplyFailed,
				"applying HTTPRoute %s: %v", owner.Name, applyErr)
			break
		}
		// The cache may not have the route yet right after creating it; its watch event brings
		// us back here, and until then readiness is simply Pending.
		if err := r.Get(ctx, types.NamespacedName{Namespace: owner.Namespace, Name: owner.Name}, &existing); err == nil {
			route = &existing
		} else if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		st.RouteName, st.PlanDigest = owner.Name, plan.Digest
		r.setCondition(&owner, routingv1alpha1.ConditionRouteApplied, metav1.ConditionTrue, ReasonApplied,
			"HTTPRoute %s matches plan %s", owner.Name, plan.Digest)
	}

	depsOK, err := r.checkDependencies(ctx, &owner)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.setReady(ctx, &owner, plan, route, depsOK); err != nil {
		return ctrl.Result{}, err
	}

	r.emitTransitionEvents(&owner, before)
	if !equality.Semantic.DeepEqual(*before, owner.Status) {
		if err := r.Status().Update(ctx, &owner); err != nil {
			return ctrl.Result{}, err
		}
	}
	// A failed apply is retried with backoff; the status above already says why.
	if applyErr != nil {
		return ctrl.Result{}, applyErr
	}
	if waitingOnGateway(&owner) {
		return ctrl.Result{RequeueAfter: pendingRequeue}, nil
	}
	return ctrl.Result{}, nil
}

// waitingOnGateway reports whether Ready is only waiting for the Gateway controller to report on
// the current generations, as opposed to a failure that needs a change from someone.
func waitingOnGateway(owner *routingv1alpha1.OpenAPIRoute) bool {
	c := meta.FindStatusCondition(owner.Status.Conditions, routingv1alpha1.ConditionReady)
	return c != nil && (c.Reason == ReasonPending || c.Reason == ReasonGatewayNotProgrammed)
}

// compile reads the document and compiles it, recording SourceValid, sourceDigest, and
// notProjected. It returns a nil plan when the source is missing or invalid, and an error only
// for failures worth retrying.
func (r *OpenAPIRouteReconciler) compile(ctx context.Context, owner *routingv1alpha1.OpenAPIRoute) (*compiler.Plan, error) {
	ref := owner.Spec.DocumentRef
	key := ref.Key
	if key == "" {
		key = defaultDocumentKey
	}
	var cm corev1.ConfigMap
	if err := r.Get(ctx, types.NamespacedName{Namespace: owner.Namespace, Name: ref.Name}, &cm); err != nil {
		if !apierrors.IsNotFound(err) {
			return nil, err
		}
		r.setCondition(owner, routingv1alpha1.ConditionSourceValid, metav1.ConditionFalse, ReasonDocumentNotFound,
			"ConfigMap %s not found", ref.Name)
		return nil, nil
	}
	document, ok := []byte(cm.Data[key]), true
	if _, inData := cm.Data[key]; !inData {
		document, ok = cm.BinaryData[key]
	}
	if !ok {
		r.setCondition(owner, routingv1alpha1.ConditionSourceValid, metav1.ConditionFalse, ReasonDocumentKeyNotFound,
			"ConfigMap %s has no key %q", ref.Name, key)
		return nil, nil
	}

	sum := sha256.Sum256(document)
	owner.Status.SourceDigest = "sha256:" + hex.EncodeToString(sum[:])

	limits := r.Limits
	if limits == (compiler.Limits{}) {
		limits = compiler.DefaultLimits
	}
	plan, err := compiler.Compile(compilerInput(owner, document), limits)
	if err != nil {
		kind := compiler.KindOf(err)
		if kind == "" {
			return nil, err
		}
		r.setCondition(owner, routingv1alpha1.ConditionSourceValid, metav1.ConditionFalse, string(kind), "%s", err.Error())
		return nil, nil
	}

	owner.Status.NotProjected = nil
	for _, np := range plan.NotProjected {
		owner.Status.NotProjected = append(owner.Status.NotProjected,
			fmt.Sprintf("%s %s %s (%s)", np.Kind, np.Name, np.Method, np.OperationID))
	}
	operations := 0
	for _, rule := range plan.Rules {
		operations += len(rule.Matches)
	}
	r.setCondition(owner, routingv1alpha1.ConditionSourceValid, metav1.ConditionTrue, ReasonCompiled,
		"compiled %d operations into %d rules; %d not projected", operations, len(plan.Rules), len(plan.NotProjected))
	return plan, nil
}

// checkDependencies verifies that every referenced Service exposes the referenced port and that
// every parent Gateway exists, recording DependenciesResolved.
func (r *OpenAPIRouteReconciler) checkDependencies(ctx context.Context, owner *routingv1alpha1.OpenAPIRoute) (bool, error) {
	var missing []string
	reason := ReasonBackendNotFound
	for _, b := range backendRefs(owner) {
		var svc corev1.Service
		if err := r.Get(ctx, types.NamespacedName{Namespace: owner.Namespace, Name: b.Name}, &svc); err != nil {
			if !apierrors.IsNotFound(err) {
				return false, err
			}
			missing = append(missing, fmt.Sprintf("Service %s not found", b.Name))
			continue
		}
		if !hasPort(&svc, b.Port) {
			missing = append(missing, fmt.Sprintf("Service %s has no port %d", b.Name, b.Port))
		}
	}
	for _, p := range owner.Spec.ParentRefs {
		ns, name, isGateway := gatewayKey(owner, p)
		if !isGateway {
			continue
		}
		if ns != owner.Namespace {
			missing = append(missing, fmt.Sprintf("Gateway %s/%s: only same-namespace Gateways are supported", ns, name))
			reason = ReasonGatewayNotFound
			continue
		}
		var gw gatewayv1.Gateway
		if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &gw); err != nil {
			if !apierrors.IsNotFound(err) {
				return false, err
			}
			missing = append(missing, fmt.Sprintf("Gateway %s not found", name))
			reason = ReasonGatewayNotFound
		}
	}
	if len(missing) > 0 {
		r.setCondition(owner, routingv1alpha1.ConditionDependenciesResolved, metav1.ConditionFalse, reason, "%s", strings.Join(missing, "; "))
		return false, nil
	}
	r.setCondition(owner, routingv1alpha1.ConditionDependenciesResolved, metav1.ConditionTrue, ReasonResolved,
		"all referenced Services and Gateways exist")
	return true, nil
}

// setReady decides Ready from, in order: the source, the route itself, dependencies, and the
// Gateway controller's view of the route and Gateway for their current generations.
func (r *OpenAPIRouteReconciler) setReady(ctx context.Context, owner *routingv1alpha1.OpenAPIRoute, plan *compiler.Plan, route *gatewayv1.HTTPRoute, depsOK bool) error {
	notReady := func(reason, format string, args ...any) error {
		r.setCondition(owner, routingv1alpha1.ConditionReady, metav1.ConditionFalse, reason, format, args...)
		return nil
	}
	applied := meta.FindStatusCondition(owner.Status.Conditions, routingv1alpha1.ConditionRouteApplied)
	switch {
	case plan == nil:
		return notReady(ReasonSourceInvalid, "the source is not valid; see SourceValid")
	case applied != nil && applied.Status != metav1.ConditionTrue:
		return notReady(applied.Reason, "%s", applied.Message)
	case plan.Empty():
		return notReady(ReasonNoOperations, "there is no route to serve")
	case !depsOK:
		return notReady(ReasonDependenciesMissing, "see DependenciesResolved")
	case route == nil:
		return notReady(ReasonPending, "route not yet applied")
	}

	for _, p := range owner.Spec.ParentRefs {
		ns, name, isGateway := gatewayKey(owner, p)
		parent := findParentStatus(route, ns, name)
		if parent == nil {
			return notReady(ReasonPending, "waiting for the Gateway controller to report on HTTPRoute %s for parent %s", route.Name, name)
		}
		for _, want := range []struct{ condition, reason string }{
			{routeAcceptedCondition, ReasonRouteNotAccepted},
			{routeResolvedRefsCondition, ReasonRefsNotResolved},
		} {
			c := meta.FindStatusCondition(parent.Conditions, want.condition)
			switch {
			case c == nil || c.ObservedGeneration != route.Generation:
				return notReady(ReasonPending, "waiting for %s on HTTPRoute %s generation %d (parent %s)", want.condition, route.Name, route.Generation, name)
			case c.Status != metav1.ConditionTrue:
				return notReady(want.reason, "HTTPRoute %s parent %s: %s=%s (%s): %s", route.Name, name, want.condition, c.Status, c.Reason, c.Message)
			}
		}
		if !isGateway {
			continue
		}
		var gw gatewayv1.Gateway
		if err := r.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &gw); err != nil {
			return client.IgnoreNotFound(err)
		}
		c := meta.FindStatusCondition(gw.Status.Conditions, gatewayProgrammedCondition)
		if c == nil || c.Status != metav1.ConditionTrue || c.ObservedGeneration != gw.Generation {
			return notReady(ReasonGatewayNotProgrammed, "Gateway %s is not programmed for its current generation", name)
		}
	}
	r.setCondition(owner, routingv1alpha1.ConditionReady, metav1.ConditionTrue, ReasonReady,
		"HTTPRoute %s is accepted with resolved references, and its Gateways are programmed", route.Name)
	return nil
}

func findParentStatus(route *gatewayv1.HTTPRoute, namespace, name string) *gatewayv1.RouteParentStatus {
	for i := range route.Status.Parents {
		ps := &route.Status.Parents[i]
		ns := route.Namespace
		if ps.ParentRef.Namespace != nil {
			ns = string(*ps.ParentRef.Namespace)
		}
		if string(ps.ParentRef.Name) == name && ns == namespace {
			return ps
		}
	}
	return nil
}

func hasPort(svc *corev1.Service, port int32) bool {
	for _, p := range svc.Spec.Ports {
		if p.Port == port {
			return true
		}
	}
	return false
}

// backendRefs lists the default backend and every binding's backend, without duplicates.
func backendRefs(owner *routingv1alpha1.OpenAPIRoute) []routingv1alpha1.BackendRef {
	seen := map[routingv1alpha1.BackendRef]bool{}
	var out []routingv1alpha1.BackendRef
	for _, b := range append([]routingv1alpha1.BackendRef{owner.Spec.DefaultBackendRef}, bindingBackends(owner)...) {
		if !seen[b] {
			seen[b] = true
			out = append(out, b)
		}
	}
	return out
}

func bindingBackends(owner *routingv1alpha1.OpenAPIRoute) []routingv1alpha1.BackendRef {
	out := make([]routingv1alpha1.BackendRef, 0, len(owner.Spec.OperationBindings))
	for _, b := range owner.Spec.OperationBindings {
		out = append(out, b.BackendRef)
	}
	return out
}

func (r *OpenAPIRouteReconciler) setCondition(owner *routingv1alpha1.OpenAPIRoute, condType string, status metav1.ConditionStatus, reason, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if len(msg) > maxConditionMessageBytes {
		msg = msg[:maxConditionMessageBytes-len(conditionMessageTruncSuffix)] + conditionMessageTruncSuffix
	}
	meta.SetStatusCondition(&owner.Status.Conditions, metav1.Condition{
		Type: condType, Status: status, Reason: reason, Message: msg, ObservedGeneration: owner.Generation,
	})
}

// emitTransitionEvents records an Event when SourceValid or RouteApplied changes, so `kubectl
// describe` shows the history without an Event per reconcile.
func (r *OpenAPIRouteReconciler) emitTransitionEvents(owner *routingv1alpha1.OpenAPIRoute, before *routingv1alpha1.OpenAPIRouteStatus) {
	if r.Recorder == nil {
		return
	}
	for _, t := range []struct{ condType, action string }{
		{routingv1alpha1.ConditionSourceValid, eventActionCompile},
		{routingv1alpha1.ConditionRouteApplied, eventActionApply},
	} {
		now := meta.FindStatusCondition(owner.Status.Conditions, t.condType)
		was := meta.FindStatusCondition(before.Conditions, t.condType)
		if now == nil || (was != nil && was.Status == now.Status && was.Reason == now.Reason && was.Message == now.Message) {
			continue
		}
		eventType := corev1.EventTypeNormal
		if now.Status != metav1.ConditionTrue {
			eventType = corev1.EventTypeWarning
		}
		action := t.action
		if now.Reason == ReasonNoOperations {
			action = eventActionDelete
		}
		r.Recorder.Eventf(owner, nil, eventType, now.Reason, action, "%s", now.Message)
	}
}

// SetupWithManager registers the field indexes and watches.
func (r *OpenAPIRouteReconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctx := context.Background()
	indexer := mgr.GetFieldIndexer()
	if err := indexer.IndexField(ctx, &routingv1alpha1.OpenAPIRoute{}, indexDocument, func(o client.Object) []string {
		return []string{o.(*routingv1alpha1.OpenAPIRoute).Spec.DocumentRef.Name}
	}); err != nil {
		return err
	}
	if err := indexer.IndexField(ctx, &routingv1alpha1.OpenAPIRoute{}, indexBackends, func(o client.Object) []string {
		refs := backendRefs(o.(*routingv1alpha1.OpenAPIRoute))
		names := make([]string, 0, len(refs))
		for _, b := range refs {
			names = append(names, b.Name)
		}
		return names
	}); err != nil {
		return err
	}
	if err := indexer.IndexField(ctx, &routingv1alpha1.OpenAPIRoute{}, indexGateways, func(o client.Object) []string {
		owner := o.(*routingv1alpha1.OpenAPIRoute)
		var keys []string
		for _, p := range owner.Spec.ParentRefs {
			if ns, name, isGateway := gatewayKey(owner, p); isGateway {
				keys = append(keys, ns+"/"+name)
			}
		}
		return keys
	}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		// Status-only updates (including our own) do not change the generation.
		For(&routingv1alpha1.OpenAPIRoute{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		// By name rather than Owns(): a foreign route at the generated name must also trigger a
		// reconcile, both to report the conflict and to recover once it is removed.
		Watches(&gatewayv1.HTTPRoute{}, handler.EnqueueRequestsFromMapFunc(sameName)).
		Watches(&corev1.ConfigMap{}, r.enqueueByIndex(indexDocument, nameKey)).
		Watches(&corev1.Service{}, r.enqueueByIndex(indexBackends, nameKey)).
		Watches(&gatewayv1.Gateway{}, r.enqueueByIndex(indexGateways, namespacedKey)).
		Named("openapiroute").
		Complete(r)
}

func sameName(_ context.Context, o client.Object) []reconcile.Request {
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: o.GetNamespace(), Name: o.GetName()}}}
}

func nameKey(o client.Object) string       { return o.GetName() }
func namespacedKey(o client.Object) string { return o.GetNamespace() + "/" + o.GetName() }

// enqueueByIndex enqueues every OpenAPIRoute in the object's namespace whose index field holds
// the object's key.
func (r *OpenAPIRouteReconciler) enqueueByIndex(index string, key func(client.Object) string) handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, o client.Object) []reconcile.Request {
		var list routingv1alpha1.OpenAPIRouteList
		if err := r.List(ctx, &list, client.InNamespace(o.GetNamespace()), client.MatchingFields{index: key(o)}); err != nil {
			logf.FromContext(ctx).Error(err, "listing OpenAPIRoutes by index", "index", index)
			return nil
		}
		reqs := make([]reconcile.Request, 0, len(list.Items))
		for _, item := range list.Items {
			reqs = append(reqs, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&item)})
		}
		return reqs
	})
}
