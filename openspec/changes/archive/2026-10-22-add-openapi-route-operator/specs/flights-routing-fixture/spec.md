# Spec Delta

## Purpose

Provide the flights OpenAPI 3.2 contract, the observable backends, and the spec publication path that the operator and its acceptance suite exercise.

## ADDED Requirements

### Requirement: Flights contract describes dated flight instances
The exercise SHALL provide an OpenAPI 3.2.0 document at `examples/flights/openapi.yaml` describing flight instances. A flight instance is identified by a UUID, not by its flight number. The Flight schema SHALL use IATA airport codes and RFC 3339 timestamps.

#### Scenario: Flight schema is inspected
- **WHEN** the Flight schema is read
- **THEN** id, flightNumber, origin, destination, scheduledDeparture, scheduledArrival, and status are required
- **AND** status is one of scheduled, boarding, departed, arrived, cancelled, or diverted
- **AND** estimatedDeparture and estimatedArrival are optional

### Requirement: Contract defines the baseline operations
The contract SHALL define five operations with unique operationIds: `GET /flights` (listFlights), `POST /flights` (createFlight), `GET /flights/{flightId}` (getFlight), `PATCH /flights/{flightId}` (updateFlight), and `GET /flights/{flightId}/status` (getFlightStatus).

#### Scenario: Request schemas separate create and update
- **WHEN** the createFlight and updateFlight request bodies are inspected
- **THEN** neither allows a client-supplied id
- **AND** updateFlight is a partial object requiring at least one property

### Requirement: Backends report their identity
Two fixture Services, flight-query and flight-command, SHALL run in `flights-lab`. Each SHALL answer any method and path with a JSON response that names the backend, the request method, and the request path.

#### Scenario: Unknown method reaches a fixture directly
- **WHEN** DELETE `/flights/abc` is sent directly to flight-command
- **THEN** the response identifies flight-command, DELETE, and `/flights/abc`

### Requirement: Spec is published to a stable ConfigMap
The deployment workflow SHALL publish the spec file into a ConfigMap whose name does not change when its content changes. Republishing an edited file SHALL update that ConfigMap in place without changing the OpenAPIRoute.

#### Scenario: Edited spec is republished
- **WHEN** the spec file is edited and the flights manifests are reapplied
- **THEN** the same-named ConfigMap holds the new content
- **AND** the OpenAPIRoute object is unchanged

### Requirement: Fixtures are not mistaken for a real service
Documentation SHALL state that the fixtures provide routing evidence only. It SHALL state that there is no persistence, authentication, or request/response schema validation, and that the Gateway does not validate path parameter formats.

#### Scenario: Limitations are documented
- **WHEN** the exercise documentation is read
- **THEN** it states these limitations explicitly
