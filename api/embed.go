// Package api embeds the OpenAPI contract so the binary can serve it at /api/openapi.yaml.
package api

import _ "embed"

// OpenAPI is api/openapi/cactustrade.yaml.
//
//go:embed openapi/cactustrade.yaml
var OpenAPI []byte
