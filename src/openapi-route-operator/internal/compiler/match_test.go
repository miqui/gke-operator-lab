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
	"regexp"
	"strings"
	"testing"
)

func TestCompilePath(t *testing.T) {
	cases := []struct {
		path      string
		wantType  PathType
		wantValue string
	}{
		{"/flights", PathExact, "/flights"},
		{"/flights/{flightId}", PathRegex, `^/flights/[^/]+$`},
		{"/flights/{flightId}/status", PathRegex, `^/flights/[^/]+/status$`},
		{"/v1.0/flights/{id}", PathRegex, `^/v1\.0/flights/[^/]+$`},
		{"/a+b/{x}/(c)", PathRegex, `^/a\+b/[^/]+/\(c\)$`},
		{"/flights/", PathExact, "/flights/"},
		{"/", PathExact, "/"},
	}
	for _, tc := range cases {
		gotType, gotValue, problem := compilePath(tc.path)
		if problem != "" || gotType != tc.wantType || gotValue != tc.wantValue {
			t.Errorf("compilePath(%q) = %q %q %q, want %q %q", tc.path, gotType, gotValue, problem, tc.wantType, tc.wantValue)
		}
	}
}

func TestTemplateRegexBoundaries(t *testing.T) {
	_, pattern, _ := compilePath("/v1.0/flights/{flightId}/status")
	re := regexp.MustCompile(pattern)
	for path, want := range map[string]bool{
		"/v1.0/flights/123e4567-e89b-12d3-a456-426614174000/status": true,
		"/v1.0/flights/not-a-uuid/status":                           true, // routing is not validation
		"/v1x0/flights/abc/status":                                  false,
		"/v1.0/flights//status":                                     false,
		"/v1.0/flights/a/b/status":                                  false,
		"/v1.0/flights/abc/status/extra":                            false,
		"/v1.0/flights/abc/status/":                                 false,
		"/prefix/v1.0/flights/abc/status":                           false,
	} {
		if got := re.MatchString(path); got != want {
			t.Errorf("%s matching %q = %v, want %v", pattern, path, got, want)
		}
	}
}

func TestCompilePathRejects(t *testing.T) {
	for path, want := range map[string]string{
		"/flights/{id}.json":  "mixes a template with literal text",
		"/flights/v{version}": "mixes a template with literal text",
		"/flights/{a}{b}":     "mixes a template with literal text",
		"/flights//status":    "empty path segment",
		"/flights/./status":   "dot segments",
		"/flights/../status":  "dot segments",
		"/flights/a%2Fb":      "encoded slash",
		"/flights/a b":        "not allowed in a route path",
		"/flights/a#b":        "not allowed in a route path",
		"flights":             "must start with '/'",
	} {
		if _, _, problem := compilePath(path); !strings.Contains(problem, want) {
			t.Errorf("compilePath(%q) problem = %q, want it to mention %q", path, problem, want)
		}
	}
}

func TestToMatch(t *testing.T) {
	m, problems := toMatch(operation{Method: "PATCH", Path: "/flights/{flightId}", OperationID: "updateFlight"}, "/api/")
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	want := Match{Method: "PATCH", PathType: PathRegex, Path: `^/api/flights/[^/]+$`, OperationID: "updateFlight"}
	if m != want {
		t.Fatalf("match = %+v, want %+v", m, want)
	}

	m, problems = toMatch(operation{Method: "GET", Path: "/flights", OperationID: "listFlights"}, "/api")
	if len(problems) > 0 || m.PathType != PathExact || m.Path != "/api/flights" {
		t.Fatalf("basePath literal match = %+v %v", m, problems)
	}
}

func TestToMatchRejectsUnsupported(t *testing.T) {
	cases := []struct {
		op   operation
		want string
	}{
		{operation{Method: "QUERY", Path: "/flights/search", OperationID: "searchFlights"}, "cannot match the QUERY method"},
		{operation{Method: "PURGE", Path: "/flights/{id}", OperationID: "purgeFlight", Additional: true}, `additionalOperations method "PURGE"`},
		{operation{Method: "GET", Path: "/a", OperationID: "a", HasServers: true}, "servers override is not supported"},
		{operation{Method: "GET", Path: "/a/" + strings.Repeat("x", maxPathLength), OperationID: "long"}, "longer than 1024"},
	}
	for _, tc := range cases {
		m, problems := toMatch(tc.op, "")
		if len(problems) == 0 || !strings.Contains(strings.Join(problems, "; "), tc.want) {
			t.Errorf("toMatch(%s) = %+v %v, want a problem mentioning %q", tc.op, m, problems, tc.want)
		}
		if m != (Match{}) {
			t.Errorf("toMatch(%s) returned a match despite problems: %+v", tc.op, m)
		}
	}
}
