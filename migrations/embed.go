// Package migrations embeds the schema migrations. Every version exists in both dialects:
// sqlite/ (standalone) and mariadb/ (containers); TestMigrationParity enforces it.
package migrations

import "embed"

// FS holds sqlite/*.sql and mariadb/*.sql.
//
//go:embed sqlite/*.sql mariadb/*.sql
var FS embed.FS
