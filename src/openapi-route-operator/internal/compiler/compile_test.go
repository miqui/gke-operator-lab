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

package compiler

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"sigs.k8s.io/yaml"
)

var (
	query   = Backend{Name: "flight-query", Port: 8080}
	command = Backend{Name: "flight-command", Port: 8080}
)

// flightsInput is the sample OpenAPIRoute's binding: reads to flight-query, writes to
// flight-command.
func flightsInput(t *testing.T, doc []byte) Input {
	t.Helper()
	if doc == nil {
		doc = readFile(t, flightsContract)
	}
	return Input{
		Document:       doc,
		DefaultBackend: query,
		Bindings:       map[string]Backend{"createFlight": command, "updateFlight": command},
	}
}

func mustCompile(t *testing.T, in Input) *Plan {
	t.Helper()
	plan, err := Compile(in, DefaultLimits)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return plan
}

// describe renders a plan as "backend: METHOD type path" lines in rule order.
func describe(p *Plan) []string {
	var out []string
	for _, r := range p.Rules {
		for _, m := range r.Matches {
			out = append(out, fmt.Sprintf("%s: %s %s %s", r.Backend.Name, m.Method, m.PathType, m.Path))
		}
	}
	return out
}

func TestCompileFlightsBaseline(t *testing.T) {
	plan := mustCompile(t, flightsInput(t, nil))
	want := []string{
		"flight-command: POST Exact /flights",
		`flight-command: PATCH RegularExpression ^/flights/[^/]+$`,
		"flight-query: GET Exact /flights",
		`flight-query: GET RegularExpression ^/flights/[^/]+$`,
		`flight-query: GET RegularExpression ^/flights/[^/]+/status$`,
	}
	if got := describe(plan); !reflect.DeepEqual(got, want) {
		t.Fatalf("plan =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if len(plan.Rules) != 2 {
		t.Fatalf("got %d rules, want one per backend (2)", len(plan.Rules))
	}
}

const literalBesideTemplate = `openapi: 3.2.0
info: {title: t, version: '1'}
paths:
  /flights/search:
    get: {operationId: searchFlights, responses: {'200': {description: ok}}}
  /flights/{flightId}:
    parameters: [{name: flightId, in: path, required: true, schema: {type: string}}]
    get: {operationId: getFlight, responses: {'200': {description: ok}}}
`

func TestLiteralBesideTemplate(t *testing.T) {
	in := flightsInput(t, []byte(literalBesideTemplate))
	in.Bindings = map[string]Backend{"searchFlights": command}
	plan := mustCompile(t, in)
	want := []string{
		"flight-command: GET Exact /flights/search",
		`flight-query: GET RegularExpression ^/flights/[^/]+$`,
	}
	if got := describe(plan); !reflect.DeepEqual(got, want) {
		t.Fatalf("plan = %v, want %v", got, want)
	}
}

func TestAmbiguousTemplatesRejected(t *testing.T) {
	doc := `openapi: 3.2.0
info: {title: t, version: '1'}
paths:
  /flights/{a}/legs:
    parameters: [{name: a, in: path, required: true, schema: {type: string}}]
    get: {operationId: getLegs, responses: {'200': {description: ok}}}
  /flights/current/{b}:
    parameters: [{name: b, in: path, required: true, schema: {type: string}}]
    get: {operationId: getCurrent, responses: {'200': {description: ok}}}
    post: {operationId: postCurrent, responses: {'200': {description: ok}}}
`
	_, err := Compile(Input{Document: []byte(doc), DefaultBackend: query}, DefaultLimits)
	if KindOf(err) != UnsupportedRouting {
		t.Fatalf("kind = %q, want UnsupportedRouting (err: %v)", KindOf(err), err)
	}
	if !strings.Contains(err.Error(), "GET /flights/{…}/legs (getLegs) and GET /flights/current/{…} (getCurrent) can match the same request") {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(err.Error(), "postCurrent") {
		t.Fatalf("different methods never conflict, but got: %v", err)
	}
}

func TestOverlap(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"/x/{a}/legs", "/x/current/{b}", true},
		{"/x/{a}/{b}", "/x/{c}/legs", true},
		{"/x/{a}", "/x/{a}/status", false},
		{"/x/{a}/legs", "/x/{b}/seats", false},
		{"/x/{a}", "/y/{a}", false},
	} {
		_, pa, _ := compilePath(tc.a)
		_, pb, _ := compilePath(tc.b)
		if got := overlap(pa, pb); got != tc.want {
			t.Errorf("overlap(%s, %s) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestBindings(t *testing.T) {
	in := flightsInput(t, nil)
	in.Bindings["cancelFlight"] = command
	in.Bindings["getFlight"] = Backend{Name: "flight-query"}
	_, err := Compile(in, DefaultLimits)
	if KindOf(err) != InvalidBinding {
		t.Fatalf("kind = %q, want InvalidBinding (err: %v)", KindOf(err), err)
	}
	for _, want := range []string{
		`binding for operationId "cancelFlight", which is not in the document`,
		`binding for operationId "getFlight" needs a Service name and a port`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}

	in = flightsInput(t, nil)
	in.DefaultBackend = Backend{}
	if _, err := Compile(in, DefaultLimits); KindOf(err) != InvalidBinding {
		t.Fatalf("missing default backend: kind = %q, want InvalidBinding", KindOf(err))
	}
}

func TestUnsupportedOperationsFailWholeCompile(t *testing.T) {
	// One unsupported operation must fail the whole compile; the valid GET is not routed alone.
	_, err := Compile(Input{Document: fixture(t, "query-method.yaml"), DefaultBackend: query}, DefaultLimits)
	if KindOf(err) != UnsupportedRouting || !strings.Contains(err.Error(), "QUERY /flights/search (searchFlights)") {
		t.Fatalf("err = %v", err)
	}
	_, err = Compile(Input{Document: fixture(t, "additional-operations.yaml"), DefaultBackend: query}, DefaultLimits)
	if KindOf(err) != UnsupportedRouting || !strings.Contains(err.Error(), `additionalOperations method "PURGE"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestWebhooksAreNotRouted(t *testing.T) {
	plan := mustCompile(t, Input{Document: fixture(t, "webhooks-callbacks.yaml"), DefaultBackend: query})
	if got, want := describe(plan), []string{"flight-query: POST Exact /subscriptions"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("plan = %v, want %v", got, want)
	}
	if len(plan.NotProjected) != 2 {
		t.Fatalf("NotProjected = %+v, want the webhook and the callback", plan.NotProjected)
	}
}

func TestDeterministicPlan(t *testing.T) {
	// Same routing content, different key and entry order.
	reordered := []byte(`openapi: 3.2.0
paths:
  /flights/{flightId}/status:
    get: {operationId: getFlightStatus, responses: {'200': {description: ok}}}
    parameters: [{in: path, required: true, name: flightId, schema: {type: string}}]
  /flights/{flightId}:
    patch: {operationId: updateFlight, responses: {'200': {description: ok}}}
    get: {operationId: getFlight, responses: {'200': {description: ok}}}
    parameters: [{name: flightId, in: path, required: true, schema: {type: string}}]
  /flights:
    post: {operationId: createFlight, responses: {'201': {description: created}}}
    get: {operationId: listFlights, responses: {'200': {description: ok}}}
info: {version: '9', title: Reordered}
`)
	a := mustCompile(t, flightsInput(t, nil))
	b := mustCompile(t, flightsInput(t, reordered))
	if !reflect.DeepEqual(a.Rules, b.Rules) || a.Digest != b.Digest {
		t.Fatalf("plans differ:\n%v %s\n%v %s", describe(a), a.Digest, describe(b), b.Digest)
	}
}

func TestDigestTracksRoutingContentOnly(t *testing.T) {
	base := mustCompile(t, flightsInput(t, nil))
	contract := string(readFile(t, flightsContract))

	descriptionOnly := strings.Replace(contract, "summary: Get a flight's current status", "summary: Current status of one flight", 1)
	descriptionOnly = strings.Replace(descriptionOnly, "pattern: '^[A-Z]{3}$'", "pattern: '^[A-Z]{3}$'\n      maxLength: 3", 1)
	if descriptionOnly == contract {
		t.Fatal("test edit did not apply")
	}
	if got := mustCompile(t, flightsInput(t, []byte(descriptionOnly))); got.Digest != base.Digest {
		t.Errorf("description/schema-only edit changed the digest")
	}

	removed := strings.Replace(contract, `  /flights/{flightId}/status:
    parameters:
      - $ref: '#/components/parameters/FlightId'
    get:
      operationId: getFlightStatus
      tags: [flights]
      summary: Get a flight's current status
      responses:
        '200':
          description: Status and estimated times.
          content:
            application/json:
              schema: { $ref: '#/components/schemas/FlightStatus' }
        '404': { $ref: '#/components/responses/NotFound' }
`, "", 1)
	if removed == contract {
		t.Fatal("test edit did not apply")
	}
	got := mustCompile(t, flightsInput(t, []byte(removed)))
	if got.Digest == base.Digest {
		t.Error("removing an operation did not change the digest")
	}
	for _, line := range describe(got) {
		if strings.Contains(line, "/status") {
			t.Errorf("removed operation still routed: %s", line)
		}
	}

	rebound := flightsInput(t, nil)
	rebound.Bindings["getFlight"] = command
	if mustCompile(t, rebound).Digest == base.Digest {
		t.Error("changing a binding did not change the digest")
	}
}

func TestEmptyAPIProducesNoRules(t *testing.T) {
	plan := mustCompile(t, Input{
		Document:       []byte("openapi: 3.2.0\ninfo: {title: t, version: '1'}\npaths: {}\n"),
		DefaultBackend: query,
	})
	if !plan.Empty() || len(plan.Rules) != 0 {
		t.Fatalf("empty API produced rules: %v", describe(plan))
	}
}

func manyOperations(n int) []byte {
	var b strings.Builder
	b.WriteString("openapi: 3.2.0\ninfo: {title: t, version: '1'}\npaths:\n")
	for i := range n {
		fmt.Fprintf(&b, "  /r%d:\n    get: {operationId: op%d, responses: {'200': {description: ok}}}\n", i, i)
	}
	return []byte(b.String())
}

func TestCapacity(t *testing.T) {
	// 100 matches to one backend fit as two rules (64 + 36).
	plan := mustCompile(t, Input{Document: manyOperations(100), DefaultBackend: query})
	if len(plan.Rules) != 2 || len(plan.Rules[0].Matches) != 64 || len(plan.Rules[1].Matches) != 36 {
		t.Fatalf("unexpected packing: %d rules", len(plan.Rules))
	}

	_, err := Compile(Input{Document: manyOperations(129), DefaultBackend: query}, DefaultLimits)
	if KindOf(err) != CapacityExceeded || !strings.Contains(err.Error(), "129 matches exceed the HTTPRoute limit of 128") {
		t.Fatalf("err = %v", err)
	}

	// 17 backends need 17 rules.
	in := Input{Document: manyOperations(17), DefaultBackend: query, Bindings: map[string]Backend{}}
	for i := range 17 {
		in.Bindings[fmt.Sprintf("op%d", i)] = Backend{Name: fmt.Sprintf("svc-%02d", i), Port: 80}
	}
	_, err = Compile(in, DefaultLimits)
	if KindOf(err) != CapacityExceeded || !strings.Contains(err.Error(), "17 rules exceed the HTTPRoute limit of 16") {
		t.Fatalf("err = %v", err)
	}
}

// TestDefaultLimitsMatchCRD keeps DefaultLimits honest against the vendored Gateway API CRD
// (make gateway-api-crds), so a Gateway API bump that changes a limit fails here.
func TestDefaultLimitsMatchCRD(t *testing.T) {
	raw := readFile(t, "../../test/crds/gateway-api/gateway.networking.k8s.io_httproutes.yaml")
	var crd apiextensionsv1.CustomResourceDefinition
	if err := yaml.Unmarshal(raw, &crd); err != nil {
		t.Fatal(err)
	}
	for _, v := range crd.Spec.Versions {
		if v.Name != "v1" {
			continue
		}
		rules := v.Schema.OpenAPIV3Schema.Properties["spec"].Properties["rules"]
		matches := rules.Items.Schema.Properties["matches"]
		if got := int(*rules.MaxItems); got != DefaultLimits.MaxRules {
			t.Errorf("CRD rules maxItems = %d, DefaultLimits.MaxRules = %d", got, DefaultLimits.MaxRules)
		}
		if got := int(*matches.MaxItems); got != DefaultLimits.MaxMatchesPerRule {
			t.Errorf("CRD matches maxItems = %d, DefaultLimits.MaxMatchesPerRule = %d", got, DefaultLimits.MaxMatchesPerRule)
		}
		total := fmt.Sprintf("<= %d", DefaultLimits.MaxMatches)
		found := false
		for _, rule := range rules.XValidations {
			if strings.Contains(rule.Rule, "matches.size()") && strings.HasSuffix(strings.TrimSpace(rule.Rule), total) {
				found = true
			}
		}
		if !found {
			t.Errorf("no CRD rule bounds total matches with %q", total)
		}
		return
	}
	t.Fatal("CRD has no v1 version")
}
