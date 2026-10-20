// Package migrations embeds the numbered SQL migrations.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
