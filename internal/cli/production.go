package cli

import (
	"fmt"

	"github.com/urfave/cli/v3"
)

// productionFlags mark the database a command writes to as production. Writes
// are then refused unless --allow-production is also given; reads and dry runs
// are unaffected.
func productionFlags() []cli.Flag {
	return []cli.Flag{
		&cli.BoolFlag{
			Name:    "production",
			Usage:   "The database written to is production: refuse to write unless --allow-production is also set",
			Sources: cli.EnvVars("SEEDSTORM_PRODUCTION"),
		},
		&cli.BoolFlag{
			Name:  "allow-production",
			Usage: "Confirm writing to a database marked --production",
		},
	}
}

// refuseProductionWrite stops a write to a production database that was not
// explicitly allowed. action says what would have been written.
func refuseProductionWrite(cmd *cli.Command, action string) error {
	if !cmd.Bool("production") || cmd.Bool("allow-production") {
		return nil
	}
	return fmt.Errorf("the database is marked production (--production or SEEDSTORM_PRODUCTION): refusing to %s; pass --allow-production to confirm, or --dry-run to preview", action)
}
