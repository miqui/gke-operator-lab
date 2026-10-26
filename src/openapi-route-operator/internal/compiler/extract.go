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
	"regexp"
	"slices"
	"strings"

	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"
)

// operation is one inbound operation under `paths`, before any routing decision.
type operation struct {
	Method      string // upper case: GET, POST, ..., QUERY, or an additionalOperations key
	Path        string // as written in the document, e.g. /flights/{flightId}
	OperationID string
	Additional  bool // declared under additionalOperations
	HasServers  bool // path-item or operation-level servers override
}

func (o operation) String() string { return o.Method + " " + o.Path }

// NotProjected is an operation that is deliberately not turned into a route: webhooks and
// callbacks are requests the API makes, not requests it receives.
type NotProjected struct {
	Kind        string // "webhook" or "callback"
	Name        string // webhook name, or "<operationId>/<callback name>"
	Method      string
	OperationID string
}

var templateExpr = regexp.MustCompile(`\{([^{}]*)\}`)

// extract lists the document's inbound operations and its non-ingress ones. It reports
// document errors (InvalidDocument) and missing operationIds (UnsupportedRouting) for every
// operation at once.
func extract(doc *v3.Document) ([]operation, []NotProjected, *problems) {
	p := &problems{}
	var ops []operation
	var skipped []NotProjected

	if doc.Paths != nil {
		for path, item := range doc.Paths.PathItems.FromOldest() {
			for _, op := range pathOperations(path, item) {
				ops = append(ops, op.operation)
				checkPathParams(p, op.operation, item.Parameters, op.raw.Parameters)
				if op.raw.Callbacks == nil {
					continue
				}
				for name, cb := range op.raw.Callbacks.FromOldest() {
					for _, expr := range cb.Expression.FromOldest() {
						for _, cop := range pathOperations("", expr) {
							skipped = append(skipped, NotProjected{
								Kind: "callback", Name: op.OperationID + "/" + name,
								Method: cop.Method, OperationID: cop.OperationID,
							})
						}
					}
				}
			}
		}
	}
	if doc.Webhooks != nil {
		for name, item := range doc.Webhooks.FromOldest() {
			for _, op := range pathOperations("", item) {
				skipped = append(skipped, NotProjected{
					Kind: "webhook", Name: name, Method: op.Method, OperationID: op.OperationID,
				})
			}
		}
	}

	seen := map[string]operation{}
	for _, op := range ops {
		if op.OperationID == "" {
			p.add(UnsupportedRouting, fmt.Sprintf("%s has no operationId; every routed operation needs one", op))
			continue
		}
		if first, dup := seen[op.OperationID]; dup {
			p.add(InvalidDocument, fmt.Sprintf("operationId %q is used by both %s and %s", op.OperationID, first, op))
			continue
		}
		seen[op.OperationID] = op
	}

	slices.SortFunc(skipped, func(a, b NotProjected) int {
		return strings.Compare(a.Kind+a.Name+a.Method, b.Kind+b.Name+b.Method)
	})
	return ops, skipped, p
}

type rawOperation struct {
	operation
	raw *v3.Operation
}

// pathOperations lists a path item's operations, fixed methods first, in a stable order.
func pathOperations(path string, item *v3.PathItem) []rawOperation {
	if item == nil {
		return nil
	}
	fixed := []struct {
		method string
		op     *v3.Operation
	}{
		{"GET", item.Get}, {"PUT", item.Put}, {"POST", item.Post}, {"DELETE", item.Delete},
		{"OPTIONS", item.Options}, {"HEAD", item.Head}, {"PATCH", item.Patch}, {"TRACE", item.Trace},
		{"QUERY", item.Query},
	}
	var out []rawOperation
	add := func(method string, op *v3.Operation, additional bool) {
		out = append(out, rawOperation{
			operation: operation{
				Method: method, Path: path, OperationID: op.OperationId, Additional: additional,
				HasServers: len(item.Servers) > 0 || len(op.Servers) > 0,
			},
			raw: op,
		})
	}
	for _, f := range fixed {
		if f.op != nil {
			add(f.method, f.op, false)
		}
	}
	if item.AdditionalOperations != nil {
		for method, op := range item.AdditionalOperations.FromOldest() {
			add(method, op, true)
		}
	}
	return out
}

// checkPathParams requires every {template} in the path to be a declared, required path
// parameter, at path-item or operation level.
func checkPathParams(p *problems, op operation, itemParams, opParams []*v3.Parameter) {
	declared := map[string]bool{}
	for _, params := range [][]*v3.Parameter{itemParams, opParams} {
		for _, param := range params {
			if param != nil && param.In == "path" {
				declared[param.Name] = param.Required != nil && *param.Required
			}
		}
	}
	for _, m := range templateExpr.FindAllStringSubmatch(op.Path, -1) {
		name := m[1]
		required, ok := declared[name]
		switch {
		case name == "":
			p.add(InvalidDocument, fmt.Sprintf("%s has an empty path template {}", op))
		case !ok:
			p.add(InvalidDocument, fmt.Sprintf("%s: path parameter {%s} is not declared", op, name))
		case !required:
			p.add(InvalidDocument, fmt.Sprintf("%s: path parameter {%s} must be required", op, name))
		}
	}
}

// problems collects issues by Kind so all of them can be reported together.
type problems struct {
	byKind map[Kind][]string
}

func (p *problems) add(k Kind, msg string) {
	if p.byKind == nil {
		p.byKind = map[Kind][]string{}
	}
	p.byKind[k] = append(p.byKind[k], msg)
}

func (p *problems) merge(o *problems) {
	for k, msgs := range o.byKind {
		for _, m := range msgs {
			p.add(k, m)
		}
	}
}

// err returns the most fundamental kind of problem found, or nil. A broken document is
// reported before unsupported features, which come before binding and capacity problems: fixing
// an earlier kind can change the later ones.
func (p *problems) err() error {
	for _, k := range []Kind{InvalidDocument, UnsupportedRouting, InvalidBinding, CapacityExceeded} {
		if msgs := p.byKind[k]; len(msgs) > 0 {
			msgs = append([]string(nil), msgs...)
			slices.Sort(msgs)
			return &Error{Kind: k, Problems: msgs}
		}
	}
	return nil
}
