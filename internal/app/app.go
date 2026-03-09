package app

import (
	"github.com/AxeForging/seedstorm/internal/build"
	icli "github.com/AxeForging/seedstorm/internal/cli"
	"github.com/urfave/cli/v3"
)

// New constructs the root CLI application.
func New() *cli.Command {
	return &cli.Command{
		Name:    "seedstorm",
		Usage:   "Dynamic database seeder with schema discovery and AI enrichment",
		Version: build.Version,
		Description: `Seedstorm connects to MySQL or PostgreSQL databases, introspects the schema
(tables, columns, types, relationships, enums), and seeds them with realistic
fake data respecting foreign key constraints.

Workflow:
  1. seedstorm introspect --db mysql --dsn "..." --out schema.yaml
  2. seedstorm ai-enrich  --schema schema.yaml --out schema.enriched.yaml
  3. seedstorm seed       --db mysql --dsn "..." --schema schema.enriched.yaml`,
		Flags:    icli.GlobalFlags(),
		Before:   icli.Before,
		Commands: icli.Commands(),
	}
}
