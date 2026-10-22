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
	"errors"
	"fmt"
	"strings"
)

// Kind classifies why compilation failed. Each kind maps to a distinct status reason, so a user
// can tell a broken document from a valid one that uses something the route cannot express.
type Kind string

const (
	// InvalidDocument: not parseable, not OpenAPI 3.2, structurally invalid, or unresolvable refs.
	InvalidDocument Kind = "InvalidDocument"
	// UnsupportedRouting: a valid document using a feature HTTPRoute cannot represent exactly.
	UnsupportedRouting Kind = "UnsupportedRouting"
	// InvalidBinding: deployment bindings that do not fit the document.
	InvalidBinding Kind = "InvalidBinding"
	// CapacityExceeded: the plan does not fit in one HTTPRoute.
	CapacityExceeded Kind = "CapacityExceeded"
)

// Error is a compilation failure. Problems lists every issue found of this Kind, sorted, so the
// user can fix them in one pass rather than one per reconcile.
type Error struct {
	Kind     Kind
	Problems []string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s", e.Kind, strings.Join(e.Problems, "; "))
}

// KindOf returns the Kind of a compilation error, or "" if err is not one.
func KindOf(err error) Kind {
	if ce, ok := errors.AsType[*Error](err); ok {
		return ce.Kind
	}
	return ""
}

func newError(kind Kind, problems ...string) *Error {
	return &Error{Kind: kind, Problems: problems}
}
