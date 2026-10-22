# openapi-route-compilation Specification

## Purpose

Define how an OpenAPI 3.2 document plus deployment bindings becomes an HTTPRoute plan: what each operation turns into, what is rejected, and how results stay deterministic.

## Requirements

### Requirement: Only valid OpenAPI 3.2 documents are compiled
The compiler SHALL accept YAML or JSON documents that declare OpenAPI 3.2.x and are structurally valid, including their internal references. Any other document SHALL fail as an invalid document, separately from unsupported routing features.

#### Scenario: Malformed document
- **WHEN** the document is not parseable YAML/JSON, declares another OpenAPI version, or contains an unresolvable `$ref`
- **THEN** compilation fails with an invalid-document error that names the problem

#### Scenario: External reference
- **WHEN** the document contains a `$ref` to another file or URL
- **THEN** compilation fails and no remote content is fetched

### Requirement: Every routable operation needs an operationId
Each inbound operation SHALL have a unique operationId. A missing or duplicate operationId SHALL fail compilation, and no backend name SHALL be invented.

#### Scenario: Duplicate operationId
- **WHEN** two operations share an operationId
- **THEN** compilation fails and names both operations

### Requirement: Each operation becomes a method-and-path match
Every inbound operation SHALL compile to a match that has an explicit HTTP method. Literal paths SHALL use Exact matches. Whole-segment templates SHALL use anchored single-segment regular expressions, with all literal text escaped. A deployment basePath, if set, SHALL prefix every path.

#### Scenario: Flights baseline compiles
- **WHEN** the flights contract is compiled with the default bindings
- **THEN** the plan contains GET and POST Exact `/flights`, GET and PATCH `^/flights/[^/]+$`, and GET `^/flights/[^/]+/status$`

#### Scenario: Literal regex characters are escaped
- **WHEN** a path contains regex metacharacters such as `.` in `/v1.0/flights`
- **THEN** the generated pattern matches only the literal text

### Requirement: Backends come from deployment bindings
Each operation SHALL route to the backend named in its operationBinding, or otherwise to the defaultBackendRef. A binding that names an operationId not present in the document SHALL fail compilation.

#### Scenario: Write operations are bound to the command backend
- **WHEN** createFlight and updateFlight are bound to flight-command
- **THEN** those matches target flight-command and all other operations target flight-query

#### Scenario: Binding names an unknown operation
- **WHEN** a binding references an operationId that is not in the document
- **THEN** compilation fails and names the binding

### Requirement: Unsupported routing features are rejected, not approximated
The compiler SHALL reject any operation it cannot represent exactly, reporting it as an unsupported routing feature. It SHALL NEVER drop a match or broaden it. This covers the QUERY method, additionalOperations methods, partial-segment templates, server variables, and operation- or path-level server overrides.

#### Scenario: QUERY operation
- **WHEN** a path item defines a `query` operation
- **THEN** compilation fails as unsupported and names the operation
- **AND** no method-less match is produced

#### Scenario: Partial-segment template
- **WHEN** a path such as `/flights/{id}.json` is present
- **THEN** compilation fails as unsupported

### Requirement: Path precedence is preserved
A literal path SHALL take precedence over a template at the same position. Two templates that can match the same request SHALL be rejected when the route cannot express which one should win.

#### Scenario: Literal beside template
- **WHEN** `/flights/search` and `/flights/{flightId}` are both defined
- **THEN** the plan has an Exact match for `/flights/search` and a regex match for the template

#### Scenario: Ambiguous templates
- **WHEN** `/flights/{a}/legs` and `/flights/current/{b}` are both defined
- **THEN** compilation fails as ambiguous

### Requirement: Webhooks and callbacks are not routed
Top-level webhooks and operation callbacks SHALL be reported as non-ingress and SHALL NOT produce route matches.

#### Scenario: Document defines a webhook
- **WHEN** the document has a `webhooks` entry
- **THEN** the plan has no match for it and the result lists it as not projected

### Requirement: Plans are deterministic and digest only routing content
Identical routing inputs SHALL produce byte-identical plans, regardless of the order of keys and entries in the document. The plan digest SHALL change only when the routing content changes, not when descriptions, schemas, or examples change.

#### Scenario: Description-only edit
- **WHEN** only an operation's description or a component schema changes
- **THEN** the plan and its digest are unchanged

#### Scenario: Operation removed
- **WHEN** an operation is removed from the document
- **THEN** the plan digest changes and the plan no longer contains that match

### Requirement: Plans respect route capacity
The compiler SHALL fail, before any write, when a plan exceeds the installed HTTPRoute limits on rules, matches per rule, or total matches. It SHALL NOT shard the plan.

#### Scenario: Plan too large
- **WHEN** a document compiles to more matches than one HTTPRoute allows
- **THEN** compilation fails with a capacity error stating the limit and the count

### Requirement: An empty API produces no route
A document with no routable operations SHALL compile to an empty plan. An empty plan SHALL never be represented as an HTTPRoute with default or catch-all matches.

#### Scenario: All operations removed
- **WHEN** every operation is removed from `paths`
- **THEN** the plan is empty
