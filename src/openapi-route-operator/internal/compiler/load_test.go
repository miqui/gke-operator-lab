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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// flightsContract is the checked-in example, read in place so tests never drift from it.
const flightsContract = "../../../../examples/flights/openapi.yaml"

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	return readFile(t, filepath.Join("testdata", name))
}

func TestLoadAcceptsValidDocuments(t *testing.T) {
	docs := map[string][]byte{
		"flights contract":     readFile(t, flightsContract),
		"query method":         fixture(t, "query-method.yaml"),
		"additionalOperations": fixture(t, "additional-operations.yaml"),
		"$self and path ref":   fixture(t, "self-and-pathitem-ref.yaml"),
		"webhooks/callbacks":   fixture(t, "webhooks-callbacks.yaml"),
		"JSON input": []byte(`{"openapi":"3.2.0","info":{"title":"t","version":"1"},
			"paths":{"/a":{"get":{"operationId":"a","responses":{"200":{"description":"ok"}}}}}}`),
	}
	for name, doc := range docs {
		t.Run(name, func(t *testing.T) {
			if _, err := load(doc); err != nil {
				t.Fatalf("load: %v", err)
			}
		})
	}
}

func TestLoadRejects(t *testing.T) {
	cases := []struct {
		name string
		doc  []byte
		kind Kind
		want string // substring of one reported problem
	}{
		{"empty", []byte("  \n"), InvalidDocument, "document is empty"},
		{"malformed YAML", []byte("openapi: [3.2.0\n"), InvalidDocument, "not valid YAML or JSON"},
		{"malformed JSON", []byte(`{"openapi": "3.2.0",`), InvalidDocument, "not valid YAML or JSON"},
		{"not an object", []byte("- a\n- b\n"), InvalidDocument, "top level must be an object"},
		{"missing version", []byte("info: {title: t, version: '1'}\npaths: {}\n"), InvalidDocument, `missing "openapi"`},
		{"OpenAPI 3.1", []byte("openapi: 3.1.0\ninfo: {title: t, version: '1'}\npaths: {}\n"), InvalidDocument, `declares openapi "3.1.0"`},
		{"schema violation", []byte("openapi: 3.2.0\ninfo: {title: t}\npaths: {}\n"), InvalidDocument, "missing property 'version'"},
		{"unresolved internal ref", fixture(t, "unresolved-ref.yaml"), InvalidDocument, "DoesNotExist"},
		{"external file ref", fixture(t, "external-ref-file.yaml"), UnsupportedRouting, `/paths/~1flights: external $ref "./other.yaml#/components/pathItems/Flights"`},
		{"external URL ref", fixture(t, "external-ref-url.yaml"), UnsupportedRouting, `external $ref "https://schemas.example.test/flight.json"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := load(tc.doc)
			if got := KindOf(err); got != tc.kind {
				t.Fatalf("kind = %q, want %q (err: %v)", got, tc.kind, err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}
