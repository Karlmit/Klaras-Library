// Package migrations embeds the SQL migration files so a released binary can
// migrate its own database with no external files or goose CLI present.
package migrations

import "embed"

// FS holds every migration in this directory, in goose format.
//
//go:embed *.sql
var FS embed.FS
