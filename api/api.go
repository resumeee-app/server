// Package api hosts the canonical OpenAPI 3.1 specification and contract validation suites
// for the Resumeee HTTP API.
//
// The root specification file (openapi.yaml) acts as the single source of truth across
// the backend REST router (internal/api) and the frontend typed client (web/src/api).
//
// Test suites in this package ensure that the OpenAPI schema remains valid, synchronized,
// and structurally compliant with OpenAPI 3.1 specifications.
package api
