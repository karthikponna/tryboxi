// Package version carries build metadata.
package version

// Version is set with -ldflags at build time.
var Version = "dev"

// MinClient is the oldest client version the API supports.
const MinClient = "0.1.0"

// APIVersion is the HTTP API major version.
const APIVersion = "v1"
