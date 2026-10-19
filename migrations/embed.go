// Package migrations embeds the SQL migrations so the binary can apply them at startup.
package migrations

import "embed"

// FS holds the goose migrations, applied in file name order.
//
//go:embed *.sql
var FS embed.FS
