package cli

import (
	"context"
	"fmt"

	"github.com/AxeForging/seedstorm/internal/ai"
	"github.com/AxeForging/seedstorm/internal/logging"
	"github.com/AxeForging/seedstorm/internal/schema"
	"github.com/urfave/cli/v3"
)

func enrichCmd() *cli.Command {
	return &cli.Command{
		Name:    "ai-enrich",
		Aliases: []string{"enrich"},
		Usage:   "Use AI to enrich faker mappings with semantically meaningful values",
		Description: `Reads a schema YAML produced by 'introspect' and uses Gemini to
replace generic faker mappings with context-aware ones based on column names,
table names, and the overall database domain.

Requires GEMINI_API_KEY to be set.`,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "schema",
				Aliases: []string{"s"},
				Usage:   "Input schema YAML file",
				Value:   "schema.yaml",
			},
			&cli.StringFlag{
				Name:    "out",
				Aliases: []string{"o"},
				Usage:   "Output enriched schema YAML file",
				Value:   "schema.enriched.yaml",
			},
			&cli.StringFlag{
				Name:  "provider",
				Usage: "AI provider to use (currently: gemini)",
				Value: "gemini",
			},
			&cli.StringFlag{
				Name:    "model",
				Aliases: []string{"m"},
				Usage:   "AI model to use (e.g. gemini-2.5-flash, gemini-1.5-pro)",
				Value:   "gemini-2.5-flash",
				Sources: cli.EnvVars("SEEDSTORM_AI_MODEL"),
			},
			&cli.StringFlag{
				Name:  "prompt",
				Usage: "Optional application domain hint to improve AI suggestions (e.g. \"TacoShop\", \"HR management system\")",
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			log := logging.Log
			schemaPath := cmd.String("schema")
			out := cmd.String("out")
			provider := cmd.String("provider")
			model := cmd.String("model")
			appContext := cmd.String("prompt")

			log.Info().Str("path", schemaPath).Msg("Loading schema")
			s, err := schema.Load(schemaPath)
			if err != nil {
				return err
			}

			logEvent := log.Info().
				Str("provider", provider).
				Str("model", model).
				Int("tables", len(s.Tables))
			if appContext != "" {
				logEvent = logEvent.Str("context", appContext)
			}
			logEvent.Msg("Enriching faker mappings with AI")

			enriched, model, err := ai.EnrichFakerMappings(ctx, s, model, appContext)
			if err != nil {
				return fmt.Errorf("AI enrichment failed: %w", err)
			}

			if err := schema.Save(out, enriched); err != nil {
				return fmt.Errorf("failed to save enriched schema: %w", err)
			}

			log.Info().
				Str("model", model).
				Str("path", out).
				Msg("Enriched schema saved")

			return nil
		},
	}
}
