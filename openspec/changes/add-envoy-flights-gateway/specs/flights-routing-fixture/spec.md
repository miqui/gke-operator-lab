# Spec Delta

## Purpose

Provide a flights-specific OpenAPI 3.2 contract and observable routing fixtures that establish acceptance cases for a future spec-driven operator.

## ADDED Requirements

### Requirement: Flights contract describes dated flight instances
The exercise SHALL provide an OpenAPI 3.2.0 document for flight instances with UUID identifiers, flight numbers, three-letter uppercase IATA origin and destination codes, RFC 3339 scheduled timestamps, optional estimated timestamps, and a flight status. Flight numbers SHALL NOT serve as unique instance identifiers.

#### Scenario: Contract describes a flight
- **WHEN** the Flight schema is inspected
- **THEN** id, flightNumber, origin, destination, scheduledDeparture, scheduledArrival, and status are required
- **AND** status is one of scheduled, boarding, departed, arrived, cancelled, or diverted
- **AND** estimatedDeparture and estimatedArrival are optional

### Requirement: Contract separates read and write operations
The contract SHALL define listFlights, createFlight, getFlight, updateFlight, and getFlightStatus as unique operationIds. Creation and update request schemas SHALL exclude client assignment of id; updates SHALL use a partial request schema.

#### Scenario: Contract operations are inspected
- **WHEN** the API paths are inspected
- **THEN** they include GET and POST /flights, GET and PATCH /flights/{flightId}, and GET /flights/{flightId}/status
- **AND** flightId is a required UUID path parameter on the templated operations

### Requirement: Backend identity demonstrates method routing
GET operations SHALL route to a flight-query Service, and POST /flights and PATCH /flights/{flightId} SHALL route to a flight-command Service. Successful fixture responses SHALL expose distinct backend identities so checks can verify the actual destination.

#### Scenario: Same path uses different backends
- **WHEN** GET /flights and POST /flights are sent through the Gateway with the configured hostname
- **THEN** GET reaches flight-query and POST reaches flight-command

#### Scenario: Flight instance is updated
- **WHEN** PATCH /flights/123e4567-e89b-12d3-a456-426614174000 is sent
- **THEN** the response identifies flight-command

### Requirement: Paths are matched without prefix broadening
Literal collection paths SHALL use exact matching. Templated paths SHALL match non-empty single path segments with anchored expressions and escaped literals. Matching SHALL NOT imply validation of UUID syntax or other OpenAPI parameter constraints.

#### Scenario: Flight and status paths match
- **WHEN** GET requests target /flights/123e4567-e89b-12d3-a456-426614174000 and its /status subresource
- **THEN** both requests reach flight-query

#### Scenario: Unexpected descendant is rejected
- **WHEN** GET /flights/123e4567-e89b-12d3-a456-426614174000/status/extra is requested
- **THEN** the Gateway returns 404 without forwarding to a fixture backend

#### Scenario: Collection prefix is not a match
- **WHEN** GET /flights-extra is requested
- **THEN** the Gateway returns 404

### Requirement: Unconfigured requests have no fallback route
The fixture SHALL have no catch-all HTTPRoute. Requests with unconfigured methods, unrelated hostnames, empty template segments, or trailing slashes SHALL receive Gateway 404 responses.

#### Scenario: Undeclared method is used
- **WHEN** DELETE /flights/123e4567-e89b-12d3-a456-426614174000 is requested
- **THEN** the Gateway returns 404 rather than routing to the command backend

#### Scenario: Hostname or path shape does not match
- **WHEN** a request uses an unrelated Host header, /flights/, /flights//status, or /flights/example/
- **THEN** the Gateway returns 404

### Requirement: Fixture does not imply business or contract enforcement
Documentation SHALL identify fixture responses as routing evidence rather than a production flight service. It SHALL distinguish OpenAPI schema validation, authentication, authorization, and custom-method support from the capabilities demonstrated by the routes.

#### Scenario: Exercise limitations are inspected
- **WHEN** the exercise documentation is read
- **THEN** it states that the fixtures do not provide durable flight storage or Gateway-level request/response schema validation
- **AND** it does not claim QUERY or arbitrary custom HTTP methods are represented by standard HTTPRoute method matches
