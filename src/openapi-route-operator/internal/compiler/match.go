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
	"strings"
)

// PathType mirrors the Gateway API path match types the compiler emits.
type PathType string

const (
	PathExact PathType = "Exact"
	PathRegex PathType = "RegularExpression"
)

// Match is one operation's route match: an explicit method plus a path match.
type Match struct {
	Method      string   `json:"method"`
	PathType    PathType `json:"pathType"`
	Path        string   `json:"path"`
	Backend     Backend  `json:"backend"`
	OperationID string   `json:"operationId"`
}

// routableMethods are the methods HTTPRoute's method match accepts. QUERY and
// additionalOperations methods are not among them, so they cannot be routed exactly.
var routableMethods = map[string]bool{
	"GET": true, "HEAD": true, "POST": true, "PUT": true, "DELETE": true,
	"CONNECT": true, "OPTIONS": true, "TRACE": true, "PATCH": true,
}

// segmentRegex matches one whole path segment: what a {param} may stand for.
const segmentRegex = `[^/]+`

// maxPathLength is HTTPPathMatch.value's MaxLength.
const maxPathLength = 1024

// literalChars are the characters Gateway API allows in an Exact path (RFC 3986 pchar and '/').
var literalChars = regexp.MustCompile(`^(?:[-A-Za-z0-9/._~!$&'()*+,;=:@]|%[0-9a-fA-F]{2})*$`)

// toMatch turns one operation into a method-and-path match, without a backend. Top-level
// servers are ignored rather than rejected: they are informational here, since the OpenAPIRoute's
// hostnames and basePath decide where the API is exposed. Path- and operation-level server
// overrides are rejected, because one route cannot send some operations elsewhere.
func toMatch(op operation, basePath string) (Match, []string) {
	var unsupported []string
	switch {
	case op.Method == "QUERY":
		unsupported = append(unsupported, fmt.Sprintf("%s (%s): HTTPRoute cannot match the QUERY method", op, op.OperationID))
	case op.Additional:
		unsupported = append(unsupported, fmt.Sprintf("%s (%s): additionalOperations method %q cannot be matched by HTTPRoute", op, op.OperationID, op.Method))
	case !routableMethods[op.Method]:
		unsupported = append(unsupported, fmt.Sprintf("%s (%s): method cannot be matched by HTTPRoute", op, op.OperationID))
	}
	if op.HasServers {
		unsupported = append(unsupported, fmt.Sprintf("%s (%s): servers override is not supported; the OpenAPIRoute's hostnames and basePath apply to every operation", op, op.OperationID))
	}

	full := strings.TrimSuffix(basePath, "/") + op.Path
	pathType, value, problem := compilePath(full)
	if problem != "" {
		unsupported = append(unsupported, fmt.Sprintf("%s (%s): %s", op, op.OperationID, problem))
	}
	if len(value) > maxPathLength {
		unsupported = append(unsupported, fmt.Sprintf("%s (%s): path match is longer than %d characters", op, op.OperationID, maxPathLength))
	}
	if len(unsupported) > 0 {
		return Match{}, unsupported
	}
	return Match{Method: op.Method, PathType: pathType, Path: value, OperationID: op.OperationID}, nil
}

// compilePath returns an Exact match for a literal path, or an anchored regex where each
// {param} matches exactly one non-empty segment and all literal text is escaped.
func compilePath(path string) (PathType, string, string) {
	if !strings.HasPrefix(path, "/") {
		return "", "", "path must start with '/'"
	}
	segments := strings.Split(path[1:], "/")
	templated := false
	parts := make([]string, len(segments))
	for i, seg := range segments {
		last := i == len(segments)-1
		switch {
		case seg == "" && !last:
			return "", "", "empty path segment ('//') cannot be matched reliably"
		case seg == "." || seg == "..":
			return "", "", "dot segments ('.', '..') cannot be matched reliably"
		case isTemplate(seg):
			templated = true
			parts[i] = segmentRegex
		case strings.ContainsAny(seg, "{}"):
			return "", "", fmt.Sprintf("segment %q mixes a template with literal text; only whole-segment templates are supported", seg)
		case strings.Contains(strings.ToLower(seg), "%2f"):
			return "", "", "encoded slash ('%2F') cannot be matched reliably"
		case !literalChars.MatchString(seg):
			return "", "", fmt.Sprintf("segment %q contains characters not allowed in a route path", seg)
		default:
			parts[i] = regexp.QuoteMeta(seg)
		}
	}
	if !templated {
		return PathExact, path, ""
	}
	return PathRegex, "^/" + strings.Join(parts, "/") + "$", ""
}

func isTemplate(seg string) bool {
	return len(seg) > 2 && seg[0] == '{' && seg[len(seg)-1] == '}' && !strings.ContainsAny(seg[1:len(seg)-1], "{}")
}
