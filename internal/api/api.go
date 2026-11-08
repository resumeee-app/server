// Package api implements the REST HTTP API for Resumeee, strictly adhering to the
// OpenAPI 3.1 specification.
//
// It provides HTTP routers, middleware (CORS, content type negotiation, structured
// logging, panic recovery, and timeout handling), and request/response handlers for:
//   - Profile management and career graph querying (/api/v1/profile)
//   - Main profile snapshot imports and exports (/api/v1/profile/jsonresume/*)
//   - Asynchronous PDF resume parsing jobs (/api/v1/profile/pdf/*)
//   - Resume branch creation, updates, and synchronization (/api/v1/branches/*)
//   - Resume version generation, iteration, and rendering (/api/v1/branches/{id}/versions/*)
//   - Multi-format template previews and rendering (/api/v1/templates/*)
//   - Tailoring preset configurations (/api/v1/presets/*)
//   - LLM model enumeration (/api/v1/models)
//
// Handlers communicate with underlying domain services, persistence repositories,
// and asynchronous worker pools via decoupled interfaces.
package api
