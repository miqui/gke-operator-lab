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
	"bytes"
	_ "embed"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"

	"github.com/pb33f/libopenapi"
	"github.com/pb33f/libopenapi/datamodel"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"sigs.k8s.io/yaml"
)

// The official OAS 3.2 JSON Schema. It validates document structure but not the Schema Objects
// inside it, which routing never looks at.
//
//go:embed schema/oas-3.2-schema-2026-08-30.json
var oasSchemaJSON []byte

const oasSchemaID = "https://spec.openapis.org/oas/3.2/schema/2026-08-30"

// maxSchemaProblems caps how many schema violations are reported; one bad subtree can produce
// dozens of near-duplicates through oneOf branches.
const maxSchemaProblems = 10

var oasSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(oasSchemaJSON))
	if err != nil {
		return nil, err
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(oasSchemaID, doc); err != nil {
		return nil, err
	}
	return c.Compile(oasSchemaID)
})

// load parses and validates an OpenAPI 3.2 document and builds its model. Checks run from
// cheapest to most expensive, and each failure stops the pipeline: a document that is not even
// YAML has no meaningful schema errors.
func load(doc []byte) (*v3.Document, error) {
	if len(bytes.TrimSpace(doc)) == 0 {
		return nil, newError(InvalidDocument, "document is empty")
	}
	asJSON, err := yaml.YAMLToJSON(doc)
	if err != nil {
		return nil, newError(InvalidDocument, fmt.Sprintf("not valid YAML or JSON: %v", err))
	}
	tree, err := jsonschema.UnmarshalJSON(bytes.NewReader(asJSON))
	if err != nil {
		return nil, newError(InvalidDocument, fmt.Sprintf("not valid YAML or JSON: %v", err))
	}
	root, ok := tree.(map[string]any)
	if !ok {
		return nil, newError(InvalidDocument, "top level must be an object")
	}
	switch v, _ := root["openapi"].(string); {
	case v == "":
		return nil, newError(InvalidDocument, `missing "openapi" version field`)
	case !strings.HasPrefix(v, "3.2."):
		return nil, newError(InvalidDocument, fmt.Sprintf("declares openapi %q; only 3.2.x is supported", v))
	}

	schema, err := oasSchema()
	if err != nil {
		return nil, fmt.Errorf("compiling embedded OAS 3.2 schema: %w", err)
	}
	if err := schema.Validate(tree); err != nil {
		return nil, newError(InvalidDocument, schemaProblems(err)...)
	}

	// Checked before building: libopenapi reports an external ref as a missing component, which
	// would be indistinguishable from a broken internal ref. Nothing is ever fetched either way.
	if refs := externalRefs(tree, ""); len(refs) > 0 {
		return nil, newError(UnsupportedRouting, refs...)
	}

	parsed, err := libopenapi.NewDocumentWithConfiguration(doc, &datamodel.DocumentConfiguration{
		Logger: slog.New(slog.DiscardHandler), // errors are returned; never log document content
	})
	if err != nil {
		return nil, newError(InvalidDocument, lines(err)...)
	}
	// BuildV3Model can return a partial model alongside an error; never route from it.
	model, err := parsed.BuildV3Model()
	if err != nil {
		return nil, newError(InvalidDocument, lines(err)...)
	}
	return &model.Model, nil
}

// externalRefs returns every $ref that does not point inside this document, as
// "<json pointer>: external $ref <value> is not supported", sorted.
func externalRefs(node any, pointer string) []string {
	var out []string
	switch n := node.(type) {
	case map[string]any:
		if ref, ok := n["$ref"].(string); ok && !strings.HasPrefix(ref, "#") {
			out = append(out, fmt.Sprintf("%s: external $ref %q is not supported", pointerOrRoot(pointer), ref))
		}
		for k, v := range n {
			out = append(out, externalRefs(v, pointer+"/"+escapePointer(k))...)
		}
	case []any:
		for i, v := range n {
			out = append(out, externalRefs(v, fmt.Sprintf("%s/%d", pointer, i))...)
		}
	}
	slices.Sort(out)
	return out
}

// schemaProblems turns a schema validation error into "at '<location>': <message>" lines.
func schemaProblems(err error) []string {
	seen := map[string]bool{}
	var out []string
	for l := range strings.SplitSeq(err.Error(), "\n") {
		l = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "-"))
		if !strings.HasPrefix(l, "at '") || seen[l] {
			continue
		}
		seen[l] = true
		out = append(out, "schema violation "+l)
	}
	if len(out) == 0 {
		return []string{"does not match the OpenAPI 3.2 schema"}
	}
	slices.Sort(out)
	if len(out) > maxSchemaProblems {
		out = append(out[:maxSchemaProblems], fmt.Sprintf("and %d more", len(out)-maxSchemaProblems))
	}
	return out
}

func lines(err error) []string {
	var out []string
	for l := range strings.SplitSeq(err.Error(), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

func escapePointer(s string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(s)
}

func pointerOrRoot(p string) string {
	if p == "" {
		return "/"
	}
	return p
}
