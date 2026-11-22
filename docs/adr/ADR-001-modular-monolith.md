# ADR-001: Hexagonal modular monolith, one binary, OpenAPI-first

**Status:** accepted (2026-11-22)

**Context.** cactusTrade must run as a single static binary on Linux, macOS and Windows and as
one container image, with few dependencies, like the other cactus apps.

**Decision.** One Go module and one binary. Domain, app and ports are kept apart by depguard.
The API is designed first in `api/openapi/cactustrade.yaml`; `oapi-codegen` generates a
standard-library `net/http` server (Go 1.22 `ServeMux`, no web framework). The React app is
built by Vite and embedded. Swagger UI is bundled into the web build (no CDN), so the strict
CSP holds.

**Consequences.** One process means one trading agent per install (the k8s Deployment pins
`replicas: 1`). Contract drift is a `make check` gate.
