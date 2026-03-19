package cli

import (
	"context"

	"github.com/AxeForging/seedstorm/internal/logging"
	"github.com/urfave/cli/v3"
)

// Commands returns all subcommands to register on the root app.
func Commands() []*cli.Command {
	return []*cli.Command{
		introspectCmd(),
		enrichCmd(),
		seedCmd(),
		gapsCmd(),
		generateCmd(),
		exportCmd(),
		versionCmd(),
		completionCmd(),
	}
}

// GlobalFlags returns flags available on every command.
func GlobalFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:    "log-level",
			Usage:   "Log verbosity: debug, info, warn, error",
			Value:   "info",
			Sources: cli.EnvVars("SEEDSTORM_LOG_LEVEL"),
		},
		&cli.BoolFlag{
			Name:  "no-color",
			Usage: "Disable colored output",
		},
	}
}

// Before is the global Before hook — sets up logging before any command runs.
func Before(ctx context.Context, cmd *cli.Command) (context.Context, error) {
	level := cmd.String("log-level")
	noColor := cmd.Bool("no-color")
	logging.Setup(level, noColor)
	return ctx, nil
}
