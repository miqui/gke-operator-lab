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
	"os"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	routingv1alpha1 "github.com/miqui/gke-operator-lab/src/openapi-route-operator/api/v1alpha1"
)

const (
	gatewayName = "flights"
	queryPort   = 8080
)

var flightsDoc = func() string {
	b, err := os.ReadFile("../../../../examples/flights/openapi.yaml")
	if err != nil {
		panic(err)
	}
	return string(b)
}()

// withCancel adds DELETE /flights/{flightId} (cancelFlight) to the flights contract, as the
// APIOps walkthrough does.
var withCancel = strings.Replace(flightsDoc, `    patch:
      operationId: updateFlight`, `    delete:
      operationId: cancelFlight
      responses:
        '204': { description: Cancelled. }
    patch:
      operationId: updateFlight`, 1)

const queryOnlyDoc = `openapi: 3.2.0
info: {title: t, version: '1'}
paths:
  /flights/search:
    query: {operationId: searchFlights, responses: {'200': {description: ok}}}
`

const emptyDoc = "openapi: 3.2.0\ninfo: {title: t, version: '1'}\npaths: {}\n"

func key(name string) types.NamespacedName {
	return types.NamespacedName{Namespace: testNamespace, Name: name}
}

func publish(name, document string) {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace}}
	err := k8sClient.Get(ctx, key(name), cm)
	cm.Data = map[string]string{"openapi.yaml": document}
	if apierrors.IsNotFound(err) {
		Expect(k8sClient.Create(ctx, cm)).To(Succeed())
		return
	}
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient.Update(ctx, cm)).To(Succeed())
}

func service(name string) {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "http", Port: queryPort}}},
	}
	Expect(client.IgnoreAlreadyExists(k8sClient.Create(ctx, svc))).To(Succeed())
}

// newOpenAPIRoute is the sample binding: reads to flight-query, writes to flight-command.
func newOpenAPIRoute(name, documentName string) *routingv1alpha1.OpenAPIRoute {
	command := routingv1alpha1.BackendRef{Name: "flight-command", Port: queryPort}
	return &routingv1alpha1.OpenAPIRoute{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec: routingv1alpha1.OpenAPIRouteSpec{
			DocumentRef:       routingv1alpha1.DocumentRef{Name: documentName},
			ParentRefs:        []gatewayv1.ParentReference{{Name: gatewayName}},
			Hostnames:         []gatewayv1.Hostname{"flights.example.test"},
			DefaultBackendRef: routingv1alpha1.BackendRef{Name: "flight-query", Port: queryPort},
			OperationBindings: []routingv1alpha1.OperationBinding{
				{OperationID: "createFlight", BackendRef: command},
				{OperationID: "updateFlight", BackendRef: command},
			},
		},
	}
}

func create(name, document string) *routingv1alpha1.OpenAPIRoute {
	publish(name+"-doc", document)
	o := newOpenAPIRoute(name, name+"-doc")
	Expect(k8sClient.Create(ctx, o)).To(Succeed())
	return o
}

func status(name string) routingv1alpha1.OpenAPIRouteStatus {
	var o routingv1alpha1.OpenAPIRoute
	Expect(k8sClient.Get(ctx, key(name), &o)).To(Succeed())
	return o.Status
}

func condition(name, condType string) func() string {
	return func() string {
		c := meta.FindStatusCondition(status(name).Conditions, condType)
		if c == nil {
			return ""
		}
		return string(c.Status) + "/" + c.Reason
	}
}

func route(name string) *gatewayv1.HTTPRoute {
	var r gatewayv1.HTTPRoute
	Expect(k8sClient.Get(ctx, key(name), &r)).To(Succeed())
	return &r
}

func routeExists(name string) func() bool {
	return func() bool {
		err := k8sClient.Get(ctx, key(name), &gatewayv1.HTTPRoute{})
		Expect(client.IgnoreNotFound(err)).To(Succeed())
		return err == nil
	}
}

// matches renders a route's matches as sorted-by-rule "backend METHOD path" strings.
func matches(name string) func() []string {
	return func() []string {
		r := &gatewayv1.HTTPRoute{}
		if err := k8sClient.Get(ctx, key(name), r); err != nil {
			return nil
		}
		var out []string
		for _, rule := range r.Spec.Rules {
			for _, m := range rule.Matches {
				out = append(out, string(rule.BackendRefs[0].Name)+" "+string(*m.Method)+" "+*m.Path.Value)
			}
		}
		return out
	}
}

var baselineMatches = []string{
	"flight-command POST /flights",
	"flight-command PATCH ^/flights/[^/]+$",
	"flight-query GET /flights",
	"flight-query GET ^/flights/[^/]+$",
	"flight-query GET ^/flights/[^/]+/status$",
}

// acceptRoute writes what Envoy Gateway would: Accepted and ResolvedRefs for the route's
// current generation.
func acceptRoute(name string) {
	Eventually(func() error {
		r := route(name)
		cond := func(t string) metav1.Condition {
			return metav1.Condition{Type: t, Status: metav1.ConditionTrue, Reason: t, ObservedGeneration: r.Generation, LastTransitionTime: metav1.Now()}
		}
		r.Status.Parents = []gatewayv1.RouteParentStatus{{
			ParentRef:      gatewayv1.ParentReference{Name: gatewayName},
			ControllerName: "gateway.envoyproxy.io/gatewayclass-controller",
			Conditions:     []metav1.Condition{cond("Accepted"), cond("ResolvedRefs")},
		}}
		return k8sClient.Status().Update(ctx, r)
	}).Should(Succeed())
}

func programGateway() {
	Eventually(func() error {
		var gw gatewayv1.Gateway
		if err := k8sClient.Get(ctx, key(gatewayName), &gw); err != nil {
			return err
		}
		meta.SetStatusCondition(&gw.Status.Conditions, metav1.Condition{
			Type: "Programmed", Status: metav1.ConditionTrue, Reason: "Programmed", ObservedGeneration: gw.Generation,
		})
		return k8sClient.Status().Update(ctx, &gw)
	}).Should(Succeed())
}

var _ = Describe("OpenAPIRoute controller", Ordered, func() {
	BeforeAll(func() {
		service("flight-query")
		service("flight-command")
		Expect(k8sClient.Create(ctx, &gatewayv1.Gateway{
			ObjectMeta: metav1.ObjectMeta{Name: gatewayName, Namespace: testNamespace},
			Spec: gatewayv1.GatewaySpec{
				GatewayClassName: "flights-envoy",
				Listeners:        []gatewayv1.Listener{{Name: "http", Port: 80, Protocol: gatewayv1.HTTPProtocolType}},
			},
		})).To(Succeed())
	})

	Context("API validation (4.1)", func() {
		It("rejects an OpenAPIRoute without a document name", func() {
			o := newOpenAPIRoute("no-doc", "")
			Expect(apierrors.IsInvalid(k8sClient.Create(ctx, o))).To(BeTrue())
		})
		It("rejects an OpenAPIRoute without parentRefs", func() {
			o := newOpenAPIRoute("no-parents", "doc")
			o.Spec.ParentRefs = nil
			Expect(apierrors.IsInvalid(k8sClient.Create(ctx, o))).To(BeTrue())
		})
		It("rejects a basePath with a trailing slash", func() {
			o := newOpenAPIRoute("bad-base", "doc")
			o.Spec.BasePath = "/api/"
			Expect(apierrors.IsInvalid(k8sClient.Create(ctx, o))).To(BeTrue())
		})
		It("defaults the document key", func() {
			o := newOpenAPIRoute("default-key", "default-key-doc")
			Expect(k8sClient.Create(ctx, o)).To(Succeed())
			Expect(o.Spec.DocumentRef.Key).To(Equal("openapi.yaml"))
		})
	})

	Context("applying routes (4.2)", func() {
		It("creates an owned route from the document", func() {
			o := create("create", flightsDoc)
			Eventually(matches("create")).Should(Equal(baselineMatches))

			r := route("create")
			Expect(metav1.IsControlledBy(r, o)).To(BeTrue())
			Expect(r.Labels).To(HaveKeyWithValue(managedByLabel, fieldManager))
			Expect(r.Spec.Hostnames).To(ConsistOf(gatewayv1.Hostname("flights.example.test")))
			Eventually(condition("create", routingv1alpha1.ConditionRouteApplied)).Should(Equal("True/Applied"))
			st := status("create")
			Expect(st.PlanDigest).To(HavePrefix("sha256:"))
			Expect(r.Annotations).To(HaveKeyWithValue(planDigestAnnotation, st.PlanDigest))
			Expect(st.RouteName).To(Equal("create"))
		})

		It("follows a ConfigMap-only update without touching the OpenAPIRoute", func() {
			create("cm-update", flightsDoc)
			Eventually(matches("cm-update")).Should(HaveLen(5))
			generation := status("cm-update").ObservedGeneration

			publish("cm-update-doc", withCancel)
			Eventually(matches("cm-update")).Should(ContainElement("flight-query DELETE ^/flights/[^/]+$"))
			Expect(status("cm-update").ObservedGeneration).To(Equal(generation))
		})

		It("acknowledges a description-only edit without writing the route", func() {
			create("no-churn", flightsDoc)
			Eventually(condition("no-churn", routingv1alpha1.ConditionRouteApplied)).Should(Equal("True/Applied"))
			before := route("no-churn")
			oldSource := status("no-churn").SourceDigest

			publish("no-churn-doc", strings.Replace(flightsDoc, "summary: List flight instances", "summary: List all flight instances", 1))
			Eventually(func() string { return status("no-churn").SourceDigest }).ShouldNot(Equal(oldSource))
			Consistently(func() string { return route("no-churn").ResourceVersion }).Should(Equal(before.ResourceVersion))
			Expect(route("no-churn").Generation).To(Equal(before.Generation))
		})

		It("refuses to modify a route it does not own", func() {
			foreign := &gatewayv1.HTTPRoute{
				ObjectMeta: metav1.ObjectMeta{Name: "taken", Namespace: testNamespace, Labels: map[string]string{"owner": "someone-else"}},
				Spec: gatewayv1.HTTPRouteSpec{CommonRouteSpec: gatewayv1.CommonRouteSpec{
					ParentRefs: []gatewayv1.ParentReference{{Name: gatewayName}},
				}},
			}
			Expect(k8sClient.Create(ctx, foreign)).To(Succeed())
			before := route("taken")

			create("taken", flightsDoc)
			Eventually(condition("taken", routingv1alpha1.ConditionRouteApplied)).Should(Equal("False/NameConflict"))
			Consistently(func() string { return route("taken").ResourceVersion }).Should(Equal(before.ResourceVersion))
			Expect(route("taken").OwnerReferences).To(BeEmpty())
		})
	})

	Context("watches (4.3)", func() {
		It("reconciles when a referenced Service appears", func() {
			o := newOpenAPIRoute("late-svc", "late-svc-doc")
			o.Spec.OperationBindings = append(o.Spec.OperationBindings, routingv1alpha1.OperationBinding{
				OperationID: "getFlightStatus", BackendRef: routingv1alpha1.BackendRef{Name: "flight-status", Port: queryPort},
			})
			publish("late-svc-doc", flightsDoc)
			Expect(k8sClient.Create(ctx, o)).To(Succeed())
			Eventually(condition("late-svc", routingv1alpha1.ConditionDependenciesResolved)).Should(Equal("False/BackendNotFound"))

			service("flight-status")
			Eventually(condition("late-svc", routingv1alpha1.ConditionDependenciesResolved)).Should(Equal("True/Resolved"))
		})
	})

	Context("failure preservation and drift (4.4)", func() {
		It("keeps the previous route when an edit is invalid", func() {
			create("invalid-edit", flightsDoc)
			Eventually(matches("invalid-edit")).Should(Equal(baselineMatches))
			before := route("invalid-edit")
			planDigest := status("invalid-edit").PlanDigest

			publish("invalid-edit-doc", "openapi: [3.2.0\n")
			Eventually(condition("invalid-edit", routingv1alpha1.ConditionSourceValid)).Should(Equal("False/InvalidDocument"))
			Expect(condition("invalid-edit", routingv1alpha1.ConditionRouteApplied)()).To(Equal("False/NotApplied"))
			Expect(condition("invalid-edit", routingv1alpha1.ConditionReady)()).To(Equal("False/SourceInvalid"))
			Expect(status("invalid-edit").PlanDigest).To(Equal(planDigest))
			Consistently(func() string { return route("invalid-edit").ResourceVersion }).Should(Equal(before.ResourceVersion))
		})

		It("applies nothing from an edit that is only partly supported", func() {
			create("partial", flightsDoc)
			Eventually(matches("partial")).Should(Equal(baselineMatches))
			publish("partial-doc", strings.Replace(withCancel, "  /flights/{flightId}/status:", `  /flights/search:
    query: {operationId: searchFlights, responses: {'200': {description: ok}}}
  /flights/{flightId}/status:`, 1))
			Eventually(condition("partial", routingv1alpha1.ConditionSourceValid)).Should(Equal("False/UnsupportedRouting"))
			Consistently(matches("partial")).Should(Equal(baselineMatches))
		})

		It("creates no route from an invalid first document", func() {
			create("invalid-first", queryOnlyDoc)
			Eventually(condition("invalid-first", routingv1alpha1.ConditionSourceValid)).Should(Equal("False/UnsupportedRouting"))
			Consistently(routeExists("invalid-first")).Should(BeFalse())
			Expect(status("invalid-first").RouteName).To(BeEmpty())
		})

		It("reports a missing document", func() {
			o := newOpenAPIRoute("missing-doc", "does-not-exist")
			Expect(k8sClient.Create(ctx, o)).To(Succeed())
			Eventually(condition("missing-doc", routingv1alpha1.ConditionSourceValid)).Should(Equal("False/DocumentNotFound"))
			Expect(routeExists("missing-doc")()).To(BeFalse())
		})

		It("reverts a manual edit to the route", func() {
			create("drift", flightsDoc)
			Eventually(matches("drift")).Should(Equal(baselineMatches))
			r := route("drift")
			r.Spec.Rules = r.Spec.Rules[:1]
			r.Spec.Hostnames = []gatewayv1.Hostname{"evil.example.test"}
			Expect(k8sClient.Update(ctx, r)).To(Succeed())
			Eventually(matches("drift")).Should(Equal(baselineMatches))
			Eventually(func() []gatewayv1.Hostname { return route("drift").Spec.Hostnames }).
				Should(ConsistOf(gatewayv1.Hostname("flights.example.test")))
		})

		It("recreates a deleted route", func() {
			create("recreate", flightsDoc)
			Eventually(routeExists("recreate")).Should(BeTrue())
			uid := route("recreate").UID
			Expect(k8sClient.Delete(ctx, route("recreate"))).To(Succeed())
			Eventually(func() types.UID {
				r := &gatewayv1.HTTPRoute{}
				_ = k8sClient.Get(ctx, key("recreate"), r)
				return r.UID
			}).ShouldNot(Or(Equal(uid), BeEmpty()))
		})

		It("deletes the route when every operation is removed", func() {
			o := create("empty", flightsDoc)
			Eventually(routeExists("empty")).Should(BeTrue())

			By("rejecting the removal while bindings still name the removed operations")
			publish("empty-doc", emptyDoc)
			Eventually(condition("empty", routingv1alpha1.ConditionSourceValid)).Should(Equal("False/InvalidBinding"))
			Consistently(routeExists("empty"), time.Second).Should(BeTrue())

			By("deleting the route once the bindings are gone too")
			Expect(k8sClient.Get(ctx, key("empty"), o)).To(Succeed())
			o.Spec.OperationBindings = nil
			Expect(k8sClient.Update(ctx, o)).To(Succeed())
			Eventually(routeExists("empty")).Should(BeFalse())
			Eventually(condition("empty", routingv1alpha1.ConditionRouteApplied)).Should(Equal("True/NoOperations"))
			Expect(status("empty").RouteName).To(BeEmpty())
			Expect(condition("empty", routingv1alpha1.ConditionReady)()).To(Equal("False/NoOperations"))
		})
	})

	Context("status and readiness (4.5)", func() {
		It("reports missing backends as a dependency failure", func() {
			o := newOpenAPIRoute("no-backend", "no-backend-doc")
			o.Spec.DefaultBackendRef = routingv1alpha1.BackendRef{Name: "nope", Port: queryPort}
			publish("no-backend-doc", flightsDoc)
			Expect(k8sClient.Create(ctx, o)).To(Succeed())
			Eventually(condition("no-backend", routingv1alpha1.ConditionDependenciesResolved)).Should(Equal("False/BackendNotFound"))
			Expect(condition("no-backend", routingv1alpha1.ConditionReady)()).To(Equal("False/DependenciesMissing"))
		})

		It("reports a wrong Service port as a dependency failure", func() {
			o := newOpenAPIRoute("bad-port", "bad-port-doc")
			o.Spec.DefaultBackendRef.Port = 9999
			publish("bad-port-doc", flightsDoc)
			Expect(k8sClient.Create(ctx, o)).To(Succeed())
			Eventually(func() string {
				c := meta.FindStatusCondition(status("bad-port").Conditions, routingv1alpha1.ConditionDependenciesResolved)
				if c == nil {
					return ""
				}
				return c.Message
			}).Should(ContainSubstring("Service flight-query has no port 9999"))
		})

		It("becomes Ready only when the route is accepted for its generation and the Gateway is programmed", func() {
			create("ready", flightsDoc)
			Eventually(condition("ready", routingv1alpha1.ConditionRouteApplied)).Should(Equal("True/Applied"))
			Eventually(condition("ready", routingv1alpha1.ConditionReady)).Should(Equal("False/Pending"))

			acceptRoute("ready")
			Eventually(condition("ready", routingv1alpha1.ConditionReady)).Should(Equal("False/GatewayNotProgrammed"))

			programGateway()
			Eventually(condition("ready", routingv1alpha1.ConditionReady)).Should(Equal("True/Ready"))
			st := status("ready")
			Expect(st.SourceDigest).To(HavePrefix("sha256:"))
			Expect(st.ObservedGeneration).To(BeNumerically(">", 0))
			for _, c := range st.Conditions {
				Expect(c.Status).To(Equal(metav1.ConditionTrue), c.Type+": "+c.Message)
			}

			By("going back to Pending when the route changes and its status is stale")
			publish("ready-doc", withCancel)
			Eventually(condition("ready", routingv1alpha1.ConditionReady)).Should(Equal("False/Pending"))
			acceptRoute("ready")
			Eventually(condition("ready", routingv1alpha1.ConditionReady)).Should(Equal("True/Ready"))
		})

		It("requeues while waiting on the Gateway controller, and only then", func() {
			create("requeue", flightsDoc)
			Eventually(condition("requeue", routingv1alpha1.ConditionReady)).Should(Equal("False/Pending"))
			// Called directly (uncached client): the result, not the watch, must bound the wait.
			// The suite's manager reconciles the same object, so retry status conflicts as the
			// workqueue would.
			r := &OpenAPIRouteReconciler{Client: k8sClient, Scheme: testScheme}
			requeueAfter := func(name string) func() (time.Duration, error) {
				return func() (time.Duration, error) {
					res, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key(name)})
					return res.RequeueAfter, err
				}
			}
			Eventually(requeueAfter("requeue")).Should(Equal(pendingRequeue))

			acceptRoute("requeue")
			programGateway()
			Eventually(requeueAfter("requeue")).Should(BeZero())
			Expect(condition("requeue", routingv1alpha1.ConditionReady)()).To(Equal("True/Ready"))

			Eventually(requeueAfter("invalid-first")).Should(BeZero(), "a failure that needs a fix must not poll")
		})

		It("lists webhooks and callbacks as not projected", func() {
			doc, err := os.ReadFile("../compiler/testdata/webhooks-callbacks.yaml")
			Expect(err).NotTo(HaveOccurred())
			o := newOpenAPIRoute("webhooks", "webhooks-doc")
			o.Spec.OperationBindings = nil
			publish("webhooks-doc", string(doc))
			Expect(k8sClient.Create(ctx, o)).To(Succeed())
			Eventually(func() []string { return status("webhooks").NotProjected }).Should(ConsistOf(
				"callback subscribe/statusChanged POST (statusChangedCallback)",
				"webhook flightDelayed POST (flightDelayedWebhook)",
			))
		})
	})

	Context("single writer and RBAC (4.6)", func() {
		It("lets only one manager hold the lease", func() {
			first := newManager(true, "lease-test")
			firstCtx, stopFirst := context.WithCancel(ctx)
			go func() { defer GinkgoRecover(); Expect(first.Start(firstCtx)).To(Succeed()) }()
			Eventually(first.Elected()).Should(BeClosed())

			second := newManager(true, "lease-test")
			secondCtx, stopSecond := context.WithCancel(ctx)
			defer stopSecond()
			go func() { defer GinkgoRecover(); Expect(second.Start(secondCtx)).To(Succeed()) }()
			Consistently(second.Elected(), 3*time.Second).ShouldNot(BeClosed())

			stopFirst() // releases the lease on cancel
			Eventually(second.Elected(), 30*time.Second).Should(BeClosed())
		})

		It("generates a namespaced Role without Secret access", func() {
			b, err := os.ReadFile("../../config/rbac/role.yaml")
			Expect(err).NotTo(HaveOccurred())
			role := string(b)
			Expect(role).To(ContainSubstring("kind: Role\n"))
			Expect(role).NotTo(ContainSubstring("kind: ClusterRole"))
			Expect(role).To(ContainSubstring("namespace: flights-lab"))
			Expect(role).NotTo(ContainSubstring("secrets"))
		})
	})

	Context("restart recovery and ownership (4.7)", func() {
		It("converges after a restart with drift that happened while stopped", func() {
			o := create("restart", flightsDoc)
			Eventually(matches("restart")).Should(Equal(baselineMatches))

			stopManagerAndWait()
			r := route("restart")
			r.Spec.Rules = r.Spec.Rules[1:]
			Expect(k8sClient.Update(ctx, r)).To(Succeed())
			Consistently(matches("restart"), time.Second).ShouldNot(Equal(baselineMatches))

			startManager()
			Eventually(matches("restart")).Should(Equal(baselineMatches))

			ref := metav1.GetControllerOf(route("restart"))
			Expect(ref).NotTo(BeNil())
			Expect(ref.Kind).To(Equal("OpenAPIRoute"))
			Expect(ref.UID).To(Equal(o.UID))
			Expect(*ref.Controller).To(BeTrue())
			Expect(*ref.BlockOwnerDeletion).To(BeTrue())
		})
	})
})
