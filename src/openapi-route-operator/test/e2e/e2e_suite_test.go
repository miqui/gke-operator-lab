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

// Package e2e is the GKE acceptance suite: it edits the published OpenAPI document and the
// OpenAPIRoute on a live cluster and asserts both the operator's status and real HTTP traffic
// through Envoy Gateway. It needs the exercise deployed (README.md "OpenAPI route operator"),
// runs only against an explicitly named kube context, and restores the baseline when done.
package e2e

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	routingv1alpha1 "github.com/miqui/gke-operator-lab/src/openapi-route-operator/api/v1alpha1"
)

const (
	namespace          = "flights-lab"
	openAPIRouteName   = "flights" // also the generated HTTPRoute's name
	configMapName      = "flights-openapi"
	documentKey        = "openapi.yaml"
	publishManager     = "flights-publish" // same field manager as flights-publish.sh
	hostname           = "flights.example.test"
	operatorDeployment = "openapi-route-operator-controller-manager"
	queryBackend       = "flight-query"
	commandBackend     = "flight-command"
	backendPort        = 8080
	flightID           = "123e4567-e89b-12d3-a456-426614174000"
	artifactsDir       = "artifacts"
)

var (
	ctx          = context.Background()
	kubeContext  string
	k8s          client.Client
	gatewayURL   string
	portForward  *exec.Cmd
	baselineDoc  []byte
	baselineSpec routingv1alpha1.OpenAPIRouteSpec
	httpClient   = &http.Client{
		Timeout:       5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
)

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	SetDefaultEventuallyTimeout(90 * time.Second)
	SetDefaultEventuallyPollingInterval(500 * time.Millisecond)
	SetDefaultConsistentlyDuration(5 * time.Second)
	SetDefaultConsistentlyPollingInterval(500 * time.Millisecond)
	RunSpecs(t, "GKE acceptance suite")
}

var _ = BeforeSuite(func() {
	// Fail, never skip: a missing context must not look like a passing run.
	kubeContext = os.Getenv("KUBE_CONTEXT")
	Expect(kubeContext).NotTo(BeEmpty(),
		"KUBE_CONTEXT is required, e.g. make test-e2e KUBE_CONTEXT=gke_dev1-486920_us-central1-a_dev-cluster; the suite never uses the current context implicitly")

	cfg, err := config.GetConfigWithContext(kubeContext)
	Expect(err).NotTo(HaveOccurred())
	scheme := runtime.NewScheme()
	Expect(clientgoscheme.AddToScheme(scheme)).To(Succeed())
	Expect(gatewayv1.Install(scheme)).To(Succeed())
	Expect(routingv1alpha1.AddToScheme(scheme)).To(Succeed())
	k8s, err = client.New(cfg, client.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred())

	baselineDoc, err = os.ReadFile("../../../../examples/flights/openapi.yaml")
	Expect(err).NotTo(HaveOccurred())

	By("checking the exercise is deployed and Ready")
	var o routingv1alpha1.OpenAPIRoute
	Expect(k8s.Get(ctx, key(openAPIRouteName), &o)).To(Succeed(),
		"OpenAPIRoute %s/%s must exist; deploy the exercise first", namespace, openAPIRouteName)
	baselineSpec = *o.Spec.DeepCopy()
	restoreBaseline()

	startPortForward()
})

var _ = AfterSuite(func() {
	if k8s != nil {
		By("restoring the baseline document and OpenAPIRoute")
		restoreBaseline()
	}
	if portForward != nil && portForward.Process != nil {
		_ = portForward.Process.Kill()
	}
})

// On failure, capture what is needed to diagnose it without the cluster in that state.
var _ = AfterEach(func() {
	if CurrentSpecReport().Failed() {
		captureDiagnostics(CurrentSpecReport().FullText())
	}
})

func key(name string) types.NamespacedName {
	return types.NamespacedName{Namespace: namespace, Name: name}
}

// ---- Gateway traffic -------------------------------------------------------------------------

// startPortForward forwards a random loopback port to the exercise Gateway's proxy Service.
func startPortForward() {
	var svcs corev1.ServiceList
	Expect(k8s.List(ctx, &svcs, client.InNamespace("envoy-gateway-system"), client.MatchingLabels{
		"gateway.envoyproxy.io/owning-gateway-namespace": namespace,
		"gateway.envoyproxy.io/owning-gateway-name":      "flights",
	})).To(Succeed())
	Expect(svcs.Items).To(HaveLen(1), "exactly one proxy Service for Gateway flights")

	portForward = exec.Command("kubectl", "--context", kubeContext, "-n", "envoy-gateway-system",
		"port-forward", "--address", "127.0.0.1", "svc/"+svcs.Items[0].Name, ":80")
	stdout, err := portForward.StdoutPipe()
	Expect(err).NotTo(HaveOccurred())
	portForward.Stderr = GinkgoWriter
	Expect(portForward.Start()).To(Succeed())

	ready := make(chan string, 1)
	go func() {
		forwarding := regexp.MustCompile(`Forwarding from 127\.0\.0\.1:(\d+)`)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if m := forwarding.FindStringSubmatch(scanner.Text()); m != nil {
				ready <- m[1]
			}
		}
	}()
	select {
	case port := <-ready:
		gatewayURL = "http://127.0.0.1:" + port
	case <-time.After(30 * time.Second):
		Fail("port-forward to the Envoy proxy did not start within 30s")
	}
}

// reply is what a request through the Gateway produced: the status, and the fixture's identity
// if a fixture answered.
type reply struct {
	Status  int
	Backend string
	Method  string
	Path    string
}

func (r reply) String() string {
	if r.Backend == "" {
		return fmt.Sprintf("%d from the gateway", r.Status)
	}
	return fmt.Sprintf("%d from %s (%s %s)", r.Status, r.Backend, r.Method, r.Path)
}

func send(method, path, host string) reply {
	req, err := http.NewRequest(method, gatewayURL+path, nil)
	Expect(err).NotTo(HaveOccurred())
	req.Host = host
	resp, err := httpClient.Do(req)
	if err != nil {
		return reply{Status: -1, Backend: err.Error()}
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	r := reply{Status: resp.StatusCode}
	var fixture struct{ Backend, Method, Path string }
	if json.Unmarshal(body, &fixture) == nil {
		r.Backend, r.Method, r.Path = fixture.Backend, fixture.Method, fixture.Path
	}
	return r
}

// routesTo waits until method+path is answered with 200 by backend.
func routesTo(method, path, backend string) {
	GinkgoHelper()
	Eventually(func() string { return send(method, path, hostname).String() }).
		Should(HavePrefix("200 from "+backend+" "), "%s %s", method, path)
}

// notRouted waits until method+path (with host) is answered by the gateway itself, never by a
// fixture, with the given status.
func notRouted(method, path, host string, status int) {
	GinkgoHelper()
	Eventually(func() reply { return send(method, path, host) }).
		Should(Equal(reply{Status: status}), "%s %s (Host: %s)", method, path, host)
}

// ---- Publishing and the OpenAPIRoute ---------------------------------------------------------

func digest(doc []byte) string {
	sum := sha256.Sum256(doc)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// publish updates the stable-name ConfigMap exactly as flights-publish.sh does.
func publish(doc []byte) {
	GinkgoHelper()
	cm := corev1ac.ConfigMap(configMapName, namespace).WithData(map[string]string{documentKey: string(doc)})
	Expect(k8s.Apply(ctx, cm, client.FieldOwner(publishManager), client.ForceOwnership)).To(Succeed())
}

func openAPIRoute() *routingv1alpha1.OpenAPIRoute {
	GinkgoHelper()
	var o routingv1alpha1.OpenAPIRoute
	Expect(k8s.Get(ctx, key(openAPIRouteName), &o)).To(Succeed())
	return &o
}

// updateSpec changes the OpenAPIRoute's spec, retrying on conflicts.
func updateSpec(mutate func(*routingv1alpha1.OpenAPIRouteSpec)) {
	GinkgoHelper()
	Expect(retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var o routingv1alpha1.OpenAPIRoute
		if err := k8s.Get(ctx, key(openAPIRouteName), &o); err != nil {
			return err
		}
		mutate(&o.Spec)
		return k8s.Update(ctx, &o)
	})).To(Succeed())
}

func binding(operationID, backend string) routingv1alpha1.OperationBinding {
	return routingv1alpha1.OperationBinding{
		OperationID: operationID,
		BackendRef:  routingv1alpha1.BackendRef{Name: backend, Port: backendPort},
	}
}

// deploy publishes doc, sets the bindings, and waits until the operator has read this exact
// document for this exact spec.
func deploy(doc []byte, bindings ...routingv1alpha1.OperationBinding) {
	GinkgoHelper()
	publish(doc)
	updateSpec(func(s *routingv1alpha1.OpenAPIRouteSpec) { s.OperationBindings = bindings })
	Eventually(func(g Gomega) {
		o := openAPIRoute()
		g.Expect(o.Status.SourceDigest).To(Equal(digest(doc)))
		g.Expect(o.Status.ObservedGeneration).To(Equal(o.Generation))
	}).Should(Succeed())
}

// condition returns "<status>/<reason>" of an OpenAPIRoute condition.
func condition(condType string) func() string {
	return func() string {
		c := meta.FindStatusCondition(openAPIRoute().Status.Conditions, condType)
		if c == nil {
			return ""
		}
		return string(c.Status) + "/" + c.Reason
	}
}

func generatedRoute() (*gatewayv1.HTTPRoute, error) {
	var r gatewayv1.HTTPRoute
	err := k8s.Get(ctx, key(openAPIRouteName), &r)
	return &r, err
}

// restoreBaseline puts the exercise back: baseline document, baseline OpenAPIRoute (recreated if a
// spec deleted it), Ready.
func restoreBaseline() {
	GinkgoHelper()
	publish(baselineDoc)
	var o routingv1alpha1.OpenAPIRoute
	if err := k8s.Get(ctx, key(openAPIRouteName), &o); client.IgnoreNotFound(err) != nil {
		Fail(err.Error())
	} else if err != nil {
		o = routingv1alpha1.OpenAPIRoute{}
		o.Name, o.Namespace, o.Spec = openAPIRouteName, namespace, *baselineSpec.DeepCopy()
		Expect(k8s.Create(ctx, &o)).To(Succeed())
	} else {
		updateSpec(func(s *routingv1alpha1.OpenAPIRouteSpec) { *s = *baselineSpec.DeepCopy() })
	}
	Eventually(condition(routingv1alpha1.ConditionReady)).Should(Equal("True/Ready"))
	Eventually(func() string { return openAPIRoute().Status.SourceDigest }).Should(Equal(digest(baselineDoc)))
}

// ---- Document variants -----------------------------------------------------------------------
// Derived from the checked-in contract so they never drift from it.

func edit(doc []byte, old, replacement string) []byte {
	GinkgoHelper()
	Expect(string(doc)).To(ContainSubstring(old), "document variant no longer applies")
	return []byte(strings.Replace(string(doc), old, replacement, 1))
}

// withCancel adds DELETE /flights/{flightId} (cancelFlight), as in PLAN.md's APIOps walkthrough.
func withCancel() []byte {
	return edit(baselineDoc, "    patch:\n      operationId: updateFlight\n", `    delete:
      operationId: cancelFlight
      summary: Cancel a flight
      responses:
        '204': { description: Cancelled. }
    patch:
      operationId: updateFlight
`)
}

// withoutStatus removes GET /flights/{flightId}/status (getFlightStatus, unbound).
func withoutStatus() []byte {
	GinkgoHelper()
	start := strings.Index(string(baselineDoc), "  /flights/{flightId}/status:\n")
	end := strings.Index(string(baselineDoc), "\ncomponents:\n")
	Expect(start).To(BeNumerically(">", 0))
	Expect(end).To(BeNumerically(">", start))
	return append(append([]byte{}, baselineDoc[:start]...), baselineDoc[end+1:]...)
}

// descriptionOnly changes only human-readable text.
func descriptionOnly() []byte {
	return edit(baselineDoc, "summary: List flight instances", "summary: List all scheduled flight instances")
}

func testdata(name string) []byte {
	GinkgoHelper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	Expect(err).NotTo(HaveOccurred())
	return b
}

// ---- Diagnostics -----------------------------------------------------------------------------

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// captureDiagnostics writes the state an engineer needs to explain a failure to
// test/e2e/artifacts/<spec>/ (gitignored).
func captureDiagnostics(spec string) {
	dir := filepath.Join(artifactsDir, strings.Trim(unsafeChars.ReplaceAllString(spec, "-"), "-"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		GinkgoWriter.Printf("diagnostics: %v\n", err)
		return
	}
	kc := func(file string, args ...string) {
		out, err := exec.Command("kubectl", append([]string{"--context", kubeContext}, args...)...).CombinedOutput()
		if err != nil {
			out = append(out, []byte("\nerror: "+err.Error())...)
		}
		_ = os.WriteFile(filepath.Join(dir, file), out, 0o644)
	}
	kc("openapiroute.yaml", "-n", namespace, "get", "openapiroute", "-o", "yaml")
	kc("httproutes.yaml", "-n", namespace, "get", "httproute", "-o", "yaml")
	kc("gateway.yaml", "-n", namespace, "get", "gateway", "flights", "-o", "yaml")
	kc("configmap.yaml", "-n", namespace, "get", "configmap", configMapName, "-o", "yaml")
	kc("events.txt", "-n", namespace, "get", "events", "--sort-by=.lastTimestamp")
	kc("operator.log", "-n", namespace, "logs", "deploy/"+operatorDeployment, "--tail=300")
	kc("envoy-gateway.log", "-n", "envoy-gateway-system", "logs", "deploy/envoy-gateway", "--tail=200")
	kc("fixtures.txt", "-n", namespace, "get", "pods,svc", "-o", "wide")
	GinkgoWriter.Printf("diagnostics written to %s\n", dir)
}
