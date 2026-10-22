//go:build e2e

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

package e2e

import (
	"os"
	"os/exec"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	routingv1alpha1 "github.com/miqui/gke-operator-lab/src/openapi-route-operator/api/v1alpha1"
)

// The baseline binding: writes to flight-command, everything else to flight-query.
var writes = []routingv1alpha1.OperationBinding{
	binding("createFlight", commandBackend),
	binding("updateFlight", commandBackend),
}

const instance = "/flights/" + flightID

var _ = Describe("Spec edits change live traffic", Ordered, func() {
	AfterAll(restoreBaseline)

	It("routes by method: GET /flights to flight-query, POST /flights to flight-command", func() {
		deploy(baselineDoc, writes...)
		routesTo("GET", "/flights", queryBackend)
		routesTo("POST", "/flights", commandBackend)
		routesTo("GET", "/flights?origin=YYZ&date=2026-11-03", queryBackend)
	})

	It("routes templated paths to their bound backends", func() {
		routesTo("GET", instance, queryBackend)
		routesTo("PATCH", instance, commandBackend)
		routesTo("GET", instance+"/status", queryBackend)
		routesTo("GET", "/flights/not-a-uuid", queryBackend) // routing is not parameter validation
	})

	It("adds an operation from the file without restarting the operator", func() {
		pod := operatorPod()
		notRouted("DELETE", instance, hostname, 404)

		deploy(withCancel(), writes...)
		routesTo("DELETE", instance, queryBackend) // unbound: the default backend

		deploy(withCancel(), append(writes, binding("cancelFlight", commandBackend))...)
		routesTo("DELETE", instance, commandBackend)
		Expect(operatorPod()).To(Equal(pod), "the operator restarted")
	})

	It("moves traffic when a binding changes", func() {
		deploy(baselineDoc, append(writes, binding("getFlight", commandBackend))...)
		routesTo("GET", instance, commandBackend)
		routesTo("GET", instance+"/status", queryBackend)

		deploy(baselineDoc, writes...)
		routesTo("GET", instance, queryBackend)
	})

	It("stops routing a removed operation", func() {
		deploy(withoutStatus(), writes...)
		notRouted("GET", instance+"/status", hostname, 404)
		routesTo("GET", instance, queryBackend)
	})

	It("keeps serving the previous route when an edit is invalid", func() {
		deploy(baselineDoc, writes...)
		Eventually(condition(routingv1alpha1.ConditionReady)).Should(Equal("True/Ready"))
		planDigest := openAPIRoute().Status.PlanDigest

		deploy(testdata("invalid.yaml"), writes...)
		Eventually(condition(routingv1alpha1.ConditionSourceValid)).Should(Equal("False/InvalidDocument"))
		Expect(condition(routingv1alpha1.ConditionReady)()).To(Equal("False/SourceInvalid"))
		Expect(openAPIRoute().Status.PlanDigest).To(Equal(planDigest))
		Consistently(func() string { return send("POST", "/flights", hostname).String() }).
			Should(HavePrefix("200 from " + commandBackend))

		deploy(testdata("unsupported-query.yaml"))
		Eventually(condition(routingv1alpha1.ConditionSourceValid)).Should(Equal("False/UnsupportedRouting"))
		Expect(openAPIRoute().Status.PlanDigest).To(Equal(planDigest))
		routesTo("PATCH", instance, commandBackend)
	})

	It("does not rewrite the route for a description-only edit", func() {
		deploy(baselineDoc, writes...)
		Eventually(condition(routingv1alpha1.ConditionRouteApplied)).Should(Equal("True/Applied"))
		before, err := generatedRoute()
		Expect(err).NotTo(HaveOccurred())

		deploy(descriptionOnly(), writes...)
		Consistently(func() string {
			r, err := generatedRoute()
			Expect(err).NotTo(HaveOccurred())
			return r.ResourceVersion
		}).Should(Equal(before.ResourceVersion))
		after, _ := generatedRoute()
		Expect(after.Generation).To(Equal(before.Generation))
	})

	It("gives a literal path precedence over a template at the same position", func() {
		deploy(testdata("literal-vs-template.yaml"), binding("searchFlights", commandBackend))
		routesTo("GET", "/flights/search", commandBackend)
		routesTo("GET", "/flights/abc", queryBackend)
		routesTo("GET", instance, queryBackend)
	})
})

var _ = Describe("Requests outside the contract never reach a backend", Ordered, func() {
	BeforeAll(func() { deploy(baselineDoc, writes...) })

	DescribeTable("gateway 404",
		func(method, path, host string) { notRouted(method, path, host, 404) },
		Entry("wrong Host", "GET", "/flights", "other.example.test"),
		Entry("undeclared method", "DELETE", instance, hostname),
		Entry("undeclared method on a collection", "PUT", "/flights", hostname),
		Entry("trailing slash", "GET", "/flights/", hostname),
		Entry("empty template segment", "GET", "/flights//status", hostname),
		Entry("unexpected descendant", "GET", instance+"/status/extra", hostname),
		Entry("collection prefix", "GET", "/flights-extra", hostname),
	)

	It("rejects an encoded slash rather than routing it", func() {
		notRouted("GET", "/flights/a%2Fb/status", hostname, 400)
	})
})

var _ = Describe("Ownership, drift, and restarts", Ordered, func() {
	const unrelated = "unrelated-route"
	var unrelatedBefore *gatewayv1.HTTPRoute

	BeforeAll(func() {
		deploy(baselineDoc, writes...)
		route := &gatewayv1.HTTPRoute{
			ObjectMeta: metav1.ObjectMeta{Name: unrelated, Namespace: namespace, Labels: map[string]string{"e2e": "unrelated"}},
			Spec: gatewayv1.HTTPRouteSpec{
				CommonRouteSpec: gatewayv1.CommonRouteSpec{ParentRefs: []gatewayv1.ParentReference{{Name: "flights"}}},
				Hostnames:       []gatewayv1.Hostname{hostname},
				Rules: []gatewayv1.HTTPRouteRule{{
					Matches: []gatewayv1.HTTPRouteMatch{{Path: &gatewayv1.HTTPPathMatch{
						Type: ptr.To(gatewayv1.PathMatchExact), Value: ptr.To("/unrelated"),
					}}},
					BackendRefs: []gatewayv1.HTTPBackendRef{{BackendRef: gatewayv1.BackendRef{
						BackendObjectReference: gatewayv1.BackendObjectReference{
							Name: queryBackend, Port: ptr.To(gatewayv1.PortNumber(backendPort)),
						},
					}}},
				}},
			},
		}
		Expect(k8s.Create(ctx, route)).To(Succeed())
		unrelatedBefore = route.DeepCopy()
		DeferCleanup(func() { _ = k8s.Delete(ctx, route) })
	})

	It("reverts a manual edit to the generated route", func() {
		r, err := generatedRoute()
		Expect(err).NotTo(HaveOccurred())
		r.Spec.Rules = r.Spec.Rules[:1]
		Expect(k8s.Update(ctx, r)).To(Succeed())
		Eventually(func() int {
			r, _ := generatedRoute()
			return len(r.Spec.Rules)
		}).Should(Equal(2))
		routesTo("GET", "/flights", queryBackend)
		routesTo("POST", "/flights", commandBackend)
	})

	It("recreates the generated route after it is deleted", func() {
		r, err := generatedRoute()
		Expect(err).NotTo(HaveOccurred())
		uid := r.UID
		Expect(k8s.Delete(ctx, r)).To(Succeed())
		Eventually(func() bool {
			r, err := generatedRoute()
			return err == nil && r.UID != uid
		}).Should(BeTrue())
		routesTo("GET", "/flights", queryBackend)
	})

	It("converges after an operator restart, including drift made while it was down", func() {
		kubectl("-n", namespace, "scale", "deployment", operatorDeployment, "--replicas=0")
		kubectl("-n", namespace, "wait", "pod", "-l", "control-plane=controller-manager", "--for=delete", "--timeout=2m")
		r, err := generatedRoute()
		Expect(err).NotTo(HaveOccurred())
		r.Spec.Rules = r.Spec.Rules[1:]
		Expect(k8s.Update(ctx, r)).To(Succeed())

		kubectl("-n", namespace, "scale", "deployment", operatorDeployment, "--replicas=1")
		kubectl("-n", namespace, "rollout", "status", "deployment/"+operatorDeployment, "--timeout=3m")
		Eventually(func() int {
			r, _ := generatedRoute()
			return len(r.Spec.Rules)
		}, "3m").Should(Equal(2))
		routesTo("POST", "/flights", commandBackend)
		Eventually(condition(routingv1alpha1.ConditionReady)).Should(Equal("True/Ready"))
	})

	It("never modifies an unrelated route, and reports a name collision instead of taking it over", func() {
		collide := &routingv1alpha1.OpenAPIRoute{
			ObjectMeta: metav1.ObjectMeta{Name: unrelated, Namespace: namespace},
			Spec:       *baselineSpec.DeepCopy(),
		}
		Expect(k8s.Create(ctx, collide)).To(Succeed())
		DeferCleanup(func() { _ = k8s.Delete(ctx, collide) })

		Eventually(func() string {
			var o routingv1alpha1.OpenAPIRoute
			Expect(k8s.Get(ctx, key(unrelated), &o)).To(Succeed())
			c := meta.FindStatusCondition(o.Status.Conditions, routingv1alpha1.ConditionRouteApplied)
			if c == nil {
				return ""
			}
			return string(c.Status) + "/" + c.Reason
		}).Should(Equal("False/NameConflict"))

		// Not resourceVersion: Envoy Gateway writes status to every attached route. What must
		// hold is that the operator never wrote to it: no field of its, no spec change, no
		// adoption, no label or annotation added.
		Consistently(func(g Gomega) {
			var r gatewayv1.HTTPRoute
			g.Expect(k8s.Get(ctx, key(unrelated), &r)).To(Succeed())
			for _, mf := range r.ManagedFields {
				g.Expect(mf.Manager).NotTo(Equal("openapi-route-operator"), "the operator wrote to an unrelated route")
			}
			g.Expect(r.Generation).To(Equal(unrelatedBefore.Generation))
			g.Expect(r.OwnerReferences).To(BeEmpty())
			g.Expect(r.Labels).To(Equal(unrelatedBefore.Labels))
			g.Expect(r.Annotations).To(Equal(unrelatedBefore.Annotations))
		}).Should(Succeed())
		routesTo("GET", "/unrelated", queryBackend)
	})
})

var _ = Describe("Readiness reflects broken references", Ordered, func() {
	AfterAll(func() { deploy(baselineDoc, writes...) })

	It("is not Ready while a bound Service is missing", func() {
		deploy(baselineDoc, append(writes, binding("getFlightStatus", "flight-status-missing"))...)
		Eventually(condition(routingv1alpha1.ConditionDependenciesResolved)).Should(Equal("False/BackendNotFound"))
		Expect(condition(routingv1alpha1.ConditionReady)()).To(Equal("False/DependenciesMissing"))
	})

	It("is Ready again once the reference is fixed", func() {
		deploy(baselineDoc, writes...)
		Eventually(condition(routingv1alpha1.ConditionReady)).Should(Equal("True/Ready"))
	})
})

var _ = Describe("Removing every route", Ordered, func() {
	AfterAll(restoreBaseline)

	It("deletes the route for an empty API instead of leaving a catch-all", func() {
		deploy(testdata("empty.yaml"))
		Eventually(func() bool {
			_, err := generatedRoute()
			return apierrors.IsNotFound(err)
		}).Should(BeTrue())
		Expect(condition(routingv1alpha1.ConditionReady)()).To(Equal("False/NoOperations"))
		notRouted("GET", "/flights", hostname, 404)
		notRouted("GET", "/", hostname, 404)
	})

	It("garbage-collects the route when the OpenAPIRoute is deleted", func() {
		deploy(baselineDoc, writes...)
		routesTo("GET", "/flights", queryBackend)

		Expect(k8s.Delete(ctx, openAPIRoute())).To(Succeed())
		Eventually(func() bool {
			_, err := generatedRoute()
			return apierrors.IsNotFound(err)
		}).Should(BeTrue())
		notRouted("GET", "/flights", hostname, 404)
		notRouted("POST", "/flights", hostname, 404)
	})
})

// E2E_SELFTEST_FAIL=1 adds a deliberately failing spec, to check that a failure leaves
// diagnostics in test/e2e/artifacts. It is not registered otherwise, so normal runs report no
// skipped specs.
var _ = func() bool {
	if os.Getenv("E2E_SELFTEST_FAIL") == "1" {
		Describe("Diagnostics self-test", func() {
			It("captures diagnostics on failure", func() {
				Expect(send("GET", "/flights", hostname).Backend).To(Equal("deliberately-wrong"))
			})
		})
	}
	return true
}()

func operatorPod() string {
	GinkgoHelper()
	out, err := exec.Command("kubectl", "--context", kubeContext, "-n", namespace, "get", "pod",
		"-l", "control-plane=controller-manager", "-o", "jsonpath={.items[*].metadata.uid}").Output()
	Expect(err).NotTo(HaveOccurred())
	return string(out)
}

func kubectl(args ...string) {
	GinkgoHelper()
	out, err := exec.Command("kubectl", append([]string{"--context", kubeContext}, args...)...).CombinedOutput()
	Expect(err).NotTo(HaveOccurred(), string(out))
}
