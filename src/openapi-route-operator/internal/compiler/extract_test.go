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
	"reflect"
	"strings"
	"testing"
)

func extractDoc(t *testing.T, doc []byte) ([]operation, []NotProjected, error) {
	t.Helper()
	model, err := load(doc)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	ops, skipped, p := extract(model)
	return ops, skipped, p.err()
}

func opStrings(ops []operation) []string {
	out := make([]string, 0, len(ops))
	for _, o := range ops {
		out = append(out, o.String()+" "+o.OperationID)
	}
	return out
}

func TestExtractFlights(t *testing.T) {
	ops, skipped, err := extractDoc(t, readFile(t, flightsContract))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"GET /flights listFlights",
		"POST /flights createFlight",
		"GET /flights/{flightId} getFlight",
		"PATCH /flights/{flightId} updateFlight",
		"GET /flights/{flightId}/status getFlightStatus",
	}
	if got := opStrings(ops); !reflect.DeepEqual(got, want) {
		t.Fatalf("operations =\n%v\nwant\n%v", got, want)
	}
	if len(skipped) != 0 {
		t.Fatalf("unexpected non-projected operations: %v", skipped)
	}
}

func TestExtractResolvesPathItemRefs(t *testing.T) {
	ops, _, err := extractDoc(t, fixture(t, "self-and-pathitem-ref.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := opStrings(ops), []string{"GET /flights listFlights"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("operations = %v, want %v", got, want)
	}
}

func TestExtractKeepsQueryAndAdditionalOperations(t *testing.T) {
	// Extraction must surface these so match generation can reject them by name; dropping
	// them here would silently lose an operation.
	ops, _, err := extractDoc(t, fixture(t, "query-method.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := opStrings(ops), []string{"GET /flights/search listSearches", "QUERY /flights/search searchFlights"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("operations = %v, want %v", got, want)
	}

	ops, _, err = extractDoc(t, fixture(t, "additional-operations.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := opStrings(ops), []string{"GET /flights/{flightId} getFlight", "PURGE /flights/{flightId} purgeFlight"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("operations = %v, want %v", got, want)
	}
	if !ops[1].Additional {
		t.Fatal("PURGE not marked as an additionalOperation")
	}
}

func TestExtractReportsWebhooksAndCallbacks(t *testing.T) {
	ops, skipped, err := extractDoc(t, fixture(t, "webhooks-callbacks.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := opStrings(ops), []string{"POST /subscriptions subscribe"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("operations = %v, want %v", got, want)
	}
	want := []NotProjected{
		{Kind: "callback", Name: "subscribe/statusChanged", Method: "POST", OperationID: "statusChangedCallback"},
		{Kind: "webhook", Name: "flightDelayed", Method: "POST", OperationID: "flightDelayedWebhook"},
	}
	if !reflect.DeepEqual(skipped, want) {
		t.Fatalf("not projected = %+v, want %+v", skipped, want)
	}
}

func TestExtractMarksServerOverrides(t *testing.T) {
	ops, _, err := extractDoc(t, []byte(`openapi: 3.2.0
info: {title: t, version: '1'}
paths:
  /a:
    servers: [{url: 'https://other.example.test'}]
    get: {operationId: a, responses: {'200': {description: ok}}}
  /b:
    get:
      operationId: b
      servers: [{url: 'https://other.example.test'}]
      responses: {'200': {description: ok}}
  /c:
    get: {operationId: c, responses: {'200': {description: ok}}}
`))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, o := range ops {
		got[o.OperationID] = o.HasServers
	}
	if want := map[string]bool{"a": true, "b": true, "c": false}; !reflect.DeepEqual(got, want) {
		t.Fatalf("HasServers = %v, want %v", got, want)
	}
}

func TestExtractRejects(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		kind Kind
		want []string
	}{
		{
			name: "missing operationIds are all reported",
			doc: `openapi: 3.2.0
info: {title: t, version: '1'}
paths:
  /a:
    get: {responses: {'200': {description: ok}}}
    post: {responses: {'200': {description: ok}}}
`,
			kind: UnsupportedRouting,
			want: []string{"GET /a has no operationId", "POST /a has no operationId"},
		},
		{
			name: "duplicate operationId",
			doc: `openapi: 3.2.0
info: {title: t, version: '1'}
paths:
  /a:
    get: {operationId: same, responses: {'200': {description: ok}}}
  /b:
    get: {operationId: same, responses: {'200': {description: ok}}}
`,
			kind: InvalidDocument,
			want: []string{`operationId "same" is used by both GET /a and GET /b`},
		},
		{
			name: "undeclared path parameter",
			doc: `openapi: 3.2.0
info: {title: t, version: '1'}
paths:
  /flights/{flightId}:
    get: {operationId: getFlight, responses: {'200': {description: ok}}}
`,
			kind: InvalidDocument,
			want: []string{"GET /flights/{flightId}: path parameter {flightId} is not declared"},
		},
		{
			name: "document errors outrank missing operationIds",
			doc: `openapi: 3.2.0
info: {title: t, version: '1'}
paths:
  /a/{x}:
    get: {responses: {'200': {description: ok}}}
`,
			kind: InvalidDocument,
			want: []string{"path parameter {x} is not declared"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := extractDoc(t, []byte(tc.doc))
			if got := KindOf(err); got != tc.kind {
				t.Fatalf("kind = %q, want %q (err: %v)", got, tc.kind, err)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q does not mention %q", err, w)
				}
			}
		})
	}
}
