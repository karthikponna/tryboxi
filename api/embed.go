// Package apispec embeds the OpenAPI contract served at /v1/openapi.yaml.
package apispec

import _ "embed"

//go:embed openapi.yaml
var OpenAPI []byte
