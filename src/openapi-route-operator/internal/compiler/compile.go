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

// Package compiler turns an OpenAPI 3.2 document plus deployment bindings into a deterministic
// HTTPRoute plan. It is pure: no Kubernetes client, no I/O, no network.
package compiler

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Backend is a same-namespace Service and port.
type Backend struct {
	Name string `json:"name"`
	Port int32  `json:"port"`
}

func (b Backend) String() string { return fmt.Sprintf("%s:%d", b.Name, b.Port) }

// Input is everything one compilation depends on.
type Input struct {
	Document       []byte
	BasePath       string             // prefixed to every path; "" for none
	DefaultBackend Backend            // for operations without a binding
	Bindings       map[string]Backend // operationId -> backend
}

// Limits bound the size of one HTTPRoute. Use DefaultLimits unless testing.
type Limits struct {
	MaxRules          int
	MaxMatchesPerRule int
	MaxMatches        int
}

// DefaultLimits are the pinned Gateway API HTTPRoute CRD's limits; a test asserts they match
// the vendored CRD so a version bump cannot drift silently.
var DefaultLimits = Limits{MaxRules: 16, MaxMatchesPerRule: 64, MaxMatches: 128}

// Rule is a set of matches sent to one backend.
type Rule struct {
	Backend Backend `json:"backend"`
	Matches []Match `json:"matches"`
}

// Plan is the compiled routing plan. An empty plan (no rules) means "no route", never a
// catch-all.
type Plan struct {
	Rules        []Rule
	NotProjected []NotProjected
	// Digest covers only what affects routing (methods, paths, backends), so description,
	// schema, example, and operationId-only edits leave it unchanged.
	Digest string
}

// Empty reports whether the plan routes nothing.
func (p *Plan) Empty() bool { return len(p.Rules) == 0 }

// Compile validates the document and bindings and produces the complete plan, or an *Error
// listing every problem of the most fundamental Kind found. It never returns a partial plan.
func Compile(in Input, limits Limits) (*Plan, error) {
	doc, err := load(in.Document)
	if err != nil {
		return nil, err
	}
	ops, notProjected, p := extract(doc)

	var matches []Match
	present := map[string]bool{}
	for _, op := range ops {
		if op.OperationID == "" {
			continue // already reported by extract
		}
		present[op.OperationID] = true
		m, unsupported := toMatch(op, in.BasePath)
		for _, u := range unsupported {
			p.add(UnsupportedRouting, u)
		}
		if len(unsupported) == 0 {
			matches = append(matches, m)
		}
	}
	p.merge(checkAmbiguity(matches))
	p.merge(bind(matches, in, present))
	if err := p.err(); err != nil {
		return nil, err
	}

	rules, capacity := group(matches, limits)
	if capacity != nil {
		return nil, capacity
	}
	return &Plan{Rules: rules, NotProjected: notProjected, Digest: digest(rules)}, nil
}

// bind assigns each match its backend and checks the bindings against the document.
func bind(matches []Match, in Input, present map[string]bool) *problems {
	p := &problems{}
	if in.DefaultBackend.Name == "" || in.DefaultBackend.Port <= 0 {
		p.add(InvalidBinding, "defaultBackendRef needs a Service name and a port")
	}
	for id, b := range in.Bindings {
		if !present[id] {
			p.add(InvalidBinding, fmt.Sprintf("binding for operationId %q, which is not in the document", id))
		}
		if b.Name == "" || b.Port <= 0 {
			p.add(InvalidBinding, fmt.Sprintf("binding for operationId %q needs a Service name and a port", id))
		}
	}
	for i := range matches {
		matches[i].Backend = in.DefaultBackend
		if b, ok := in.Bindings[matches[i].OperationID]; ok {
			matches[i].Backend = b
		}
	}
	return p
}

// checkAmbiguity rejects templated paths whose order would decide routing. Exact matches always
// win over regex matches, so a literal path beside a template is fine. Two templates with the
// same method that can match the same request are not: regex precedence is implementation-
// specific, and RE2 has no lookahead to carve one pattern out of the other.
func checkAmbiguity(matches []Match) *problems {
	p := &problems{}
	for i, a := range matches {
		for _, b := range matches[i+1:] {
			if a.Method != b.Method || a.PathType != PathRegex || b.PathType != PathRegex {
				continue
			}
			if overlap(a.Path, b.Path) {
				p.add(UnsupportedRouting, fmt.Sprintf(
					"%s (%s) and %s (%s) can match the same request; precedence between templated paths cannot be expressed",
					a.Method+" "+templateForm(a.Path), a.OperationID, b.Method+" "+templateForm(b.Path), b.OperationID))
			}
		}
	}
	return p
}

// overlap reports whether two compiled regex paths can match one request: same number of
// segments, and at each position the literals agree or at least one side is a template.
func overlap(a, b string) bool {
	as, bs := regexSegments(a), regexSegments(b)
	if len(as) != len(bs) {
		return false
	}
	for i := range as {
		if as[i] != segmentRegex && bs[i] != segmentRegex && as[i] != bs[i] {
			return false
		}
	}
	return true
}

// regexSegments splits a compiled path pattern back into segments. segmentRegex itself contains
// a '/', so it is swapped for a placeholder before splitting.
func regexSegments(pattern string) []string {
	const placeholder = "\x00"
	body := strings.TrimSuffix(strings.TrimPrefix(pattern, "^/"), "$")
	segs := strings.Split(strings.ReplaceAll(body, segmentRegex, placeholder), "/")
	for i, s := range segs {
		if s == placeholder {
			segs[i] = segmentRegex
		}
	}
	return segs
}

// templateForm renders a compiled regex back as a readable template for error messages.
func templateForm(pattern string) string {
	segs := regexSegments(pattern)
	for i, s := range segs {
		if s == segmentRegex {
			segs[i] = "{…}"
		} else {
			segs[i] = strings.ReplaceAll(s, `\`, "")
		}
	}
	return "/" + strings.Join(segs, "/")
}

// group sorts matches deterministically and packs them into rules, one or more per backend,
// then checks the route's capacity. Backends are ordered by name and port; within a rule, Exact
// matches come before regex ones, then by path and method.
func group(matches []Match, limits Limits) ([]Rule, error) {
	if len(matches) == 0 {
		return nil, nil
	}
	sorted := slices.Clone(matches)
	slices.SortFunc(sorted, func(a, b Match) int {
		return cmp.Or(
			cmp.Compare(a.Backend.Name, b.Backend.Name),
			cmp.Compare(a.Backend.Port, b.Backend.Port),
			cmp.Compare(pathTypeOrder(a.PathType), pathTypeOrder(b.PathType)),
			cmp.Compare(a.Path, b.Path),
			cmp.Compare(a.Method, b.Method),
		)
	})

	var rules []Rule
	for _, m := range sorted {
		last := len(rules) - 1
		if last < 0 || rules[last].Backend != m.Backend || len(rules[last].Matches) == limits.MaxMatchesPerRule {
			rules = append(rules, Rule{Backend: m.Backend})
			last++
		}
		rules[last].Matches = append(rules[last].Matches, m)
	}

	var over []string
	if len(sorted) > limits.MaxMatches {
		over = append(over, fmt.Sprintf("%d matches exceed the HTTPRoute limit of %d", len(sorted), limits.MaxMatches))
	}
	if len(rules) > limits.MaxRules {
		over = append(over, fmt.Sprintf("%d rules exceed the HTTPRoute limit of %d", len(rules), limits.MaxRules))
	}
	if len(over) > 0 {
		return nil, newError(CapacityExceeded, over...)
	}
	return rules, nil
}

func pathTypeOrder(t PathType) int {
	if t == PathExact {
		return 0
	}
	return 1
}

// digest hashes the routing content of the rules: backends, methods, path types, and paths.
func digest(rules []Rule) string {
	type routing struct {
		Backend Backend    `json:"b"`
		Matches [][]string `json:"m"`
	}
	view := make([]routing, len(rules))
	for i, r := range rules {
		view[i].Backend = r.Backend
		for _, m := range r.Matches {
			view[i].Matches = append(view[i].Matches, []string{m.Method, string(m.PathType), m.Path})
		}
	}
	b, err := json.Marshal(view)
	if err != nil {
		panic(err) // plain structs of strings and ints always marshal
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}
