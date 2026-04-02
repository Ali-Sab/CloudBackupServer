package migrations

import "embed"

// FS holds all SQL migration files embedded into the binary at compile time.
//
//go:embed *.sql
var FS embed.FS
