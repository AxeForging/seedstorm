package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/AxeForging/seedstorm/internal/logging"
	"github.com/AxeForging/seedstorm/internal/web"
	"github.com/urfave/cli/v3"
)

func serveCmd() *cli.Command {
	return &cli.Command{
		Name:  "serve",
		Usage: "Run the local web UI for seedstorm features",
		Description: `Starts an HTTP server on localhost that exposes a web UI for every
seedstorm feature: introspection, dependency graph, seeding, gap analysis,
generation, AI enrichment, and export.

Connection passwords are held only in process memory; non-secret connection
fields can be saved as named presets in the browser's localStorage.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "addr",
				Usage:   "Address to listen on (host:port)",
				Value:   "127.0.0.1:8080",
				Sources: cli.EnvVars("SEEDSTORM_ADDR"),
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			log := logging.Log
			addr := cmd.String("addr")
			s, err := web.New(web.Options{Addr: addr})
			if err != nil {
				return fmt.Errorf("init web server: %w", err)
			}
			log.Info().Str("addr", addr).Msg("seedstorm web UI listening")
			fmt.Fprintf(cmd.Writer, "\n  Open http://%s in your browser.\n\n", addr)
			if err := s.ListenAndServe(ctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
	}
}
