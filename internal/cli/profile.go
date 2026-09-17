package cli

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/urfave/cli/v3"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/faker"
	"github.com/AxeForging/seedstorm/internal/logging"
	"github.com/AxeForging/seedstorm/internal/profiles"
	"github.com/AxeForging/seedstorm/internal/rules"
	"github.com/AxeForging/seedstorm/internal/schema"
	"github.com/AxeForging/seedstorm/internal/tui"
)

// profilesPathEnv overrides where saved profiles live (tests, CI, shared setups).
const profilesPathEnv = "SEEDSTORM_PROFILES"

func profileFlag() cli.Flag {
	return &cli.StringFlag{
		Name:    "profile",
		Aliases: []string{"p"},
		Usage:   "Seed profile: a rules YAML file or the name of a profile saved in the web UI",
		Sources: cli.EnvVars("SEEDSTORM_PROFILE"),
	}
}

// profileStore opens the saved-profile store shared with `seedstorm serve`.
func profileStore() (*profiles.Store, error) {
	path := os.Getenv(profilesPathEnv)
	if path == "" {
		var err error
		if path, err = profiles.DefaultPath(); err != nil {
			return nil, err
		}
	}
	return profiles.NewStore(path), nil
}

// loadedProfile is a profile compiled against one schema.
type loadedProfile struct {
	rules     *rules.RuleSet
	schema    *schema.Schema
	overrides faker.Overrides
	shapes    map[string]faker.Shape
	runID     string
}

// loadProfile resolves --profile and compiles it for sc, logging warnings.
// It returns a zero value when no profile was given.
func loadProfile(cmd *cli.Command, sc *schema.Schema) (loadedProfile, error) {
	ref := cmd.String("profile")
	if ref == "" {
		return loadedProfile{}, nil
	}
	store, err := profileStore()
	if err != nil {
		return loadedProfile{}, err
	}
	rs, err := profiles.Resolve(ref, store)
	if err != nil {
		return loadedProfile{}, err
	}
	return compileProfile(rs, sc)
}

func compileProfile(rs *rules.RuleSet, sc *schema.Schema) (loadedProfile, error) {
	lp := loadedProfile{rules: rs, schema: sc, runID: rules.NewRunID()}
	log := logging.Log
	for _, issue := range rs.Validate(sc) {
		if issue.Severity == rules.SeverityWarning {
			log.Warn().Str("path", issue.Path).Msg(issue.Message)
		}
	}
	overrides, err := rs.Compile(sc, lp.runID)
	if err != nil {
		return lp, err
	}
	lp.overrides = overrides
	lp.shapes = rs.Shapes(sc)
	if len(lp.shapes) > 0 {
		log.Info().Int("relationships", len(lp.shapes)).Msg("Relationship shapes applied")
	}
	name := rs.Name
	if name == "" {
		name = "unnamed"
	}
	log.Info().Str("profile", name).Str("run", lp.runID).Int("tables", len(overrides)).Msg("Seed profile applied")
	return lp, nil
}

// tableRows layers explicit --table-rows over the profile's per-table rows.
func (lp loadedProfile) tableRows(explicit map[string]int) map[string]int {
	if lp.rules == nil {
		return explicit
	}
	return rules.MergeTableRowsFor(lp.rules, lp.schema, explicit)
}

// tui converts the profile for the interactive flows.
func (lp loadedProfile) tui() tui.Profile {
	if lp.rules == nil {
		return tui.Profile{}
	}
	name := lp.rules.Name
	if name == "" {
		name = "profile"
	}
	return tui.Profile{Name: name, Overrides: lp.overrides, Shapes: lp.shapes, TableRows: rules.MergeTableRowsFor(lp.rules, lp.schema, nil)}
}

func profileCmd() *cli.Command {
	storeFor := func() (*profiles.Store, error) { return profileStore() }
	return &cli.Command{
		Name:  "profile",
		Usage: "List, inspect, import, validate and delete saved seed profiles",
		Description: `Seed profiles are rule sets that shape generated values: column patterns with
templates such as "loadtest+{{seq}}@{{domain}}", fixed values, NULLs and per-table
row counts. Profiles saved in the web UI live in ~/.config/seedstorm/profiles.yaml
(override with SEEDSTORM_PROFILES) and can be used with --profile on seed, gaps,
generate and mirror.`,
		Commands: []*cli.Command{
			{
				Name:  "list",
				Usage: "List saved profiles",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					store, err := storeFor()
					if err != nil {
						return err
					}
					all, err := store.List()
					if err != nil {
						return err
					}
					if len(all) == 0 {
						fmt.Printf("No saved profiles in %s\n", store.Path())
						return nil
					}
					tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
					_, _ = fmt.Fprintln(tw, "NAME\tPATTERN RULES\tTABLES\tUPDATED\tID")
					for _, p := range all {
						_, _ = fmt.Fprintf(tw, "%s\t%d\t%d\t%s\t%s\n", p.Name(), len(p.Rules.Rules), len(p.Rules.Tables), p.UpdatedAt.Format("2006-01-02 15:04"), p.ID)
					}
					return tw.Flush()
				},
			},
			{
				Name:      "show",
				Usage:     "Print a saved profile as YAML (usable as a --profile file)",
				ArgsUsage: "<name>",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					store, err := storeFor()
					if err != nil {
						return err
					}
					rs, err := profiles.Resolve(cmd.Args().First(), store)
					if err != nil {
						return err
					}
					if rs == nil {
						return fmt.Errorf("profile name is required")
					}
					b, err := rs.Marshal()
					if err != nil {
						return err
					}
					fmt.Print(string(b))
					return nil
				},
			},
			{
				Name:      "import",
				Usage:     "Save a rules YAML file as a named profile (replaces a profile with the same name)",
				ArgsUsage: "<file>",
				Flags:     []cli.Flag{&cli.StringFlag{Name: "name", Usage: "Profile name (default: the file's name field)"}},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					store, err := storeFor()
					if err != nil {
						return err
					}
					rs, err := rules.Load(cmd.Args().First())
					if err != nil {
						return err
					}
					if name := cmd.String("name"); name != "" {
						rs.Name = name
					}
					id := ""
					if existing, err := store.Get(rs.Name); err == nil {
						id = existing.ID
					}
					saved, err := store.Save(id, *rs)
					if err != nil {
						return err
					}
					fmt.Printf("Saved profile %q (%s)\n", saved.Name(), saved.ID)
					return nil
				},
			},
			{
				Name:      "validate",
				Usage:     "Check a profile file or saved profile, optionally against a live database",
				ArgsUsage: "<file|name>",
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "db", Usage: "Database type: mysql or postgres", Value: "postgres"},
					&cli.StringFlag{Name: "dsn", Usage: "Validate column rules against this database's schema"},
				},
				Action: func(ctx context.Context, cmd *cli.Command) error {
					store, err := storeFor()
					if err != nil {
						return err
					}
					rs, err := profiles.Resolve(cmd.Args().First(), store)
					if err != nil {
						return err
					}
					if rs == nil {
						return fmt.Errorf("profile file or name is required")
					}
					var sc *schema.Schema
					if dsn := cmd.String("dsn"); dsn != "" {
						dbType := normalizeDBType(cmd.String("db"))
						tables, err := db.Introspect(dbType, dsn)
						if err != nil {
							return err
						}
						sc = faker.BuildSchema(dbType, tables)
					}
					issues := rs.Validate(sc)
					for _, issue := range issues {
						fmt.Println(issue.String())
					}
					if rules.HasErrors(issues) {
						return fmt.Errorf("profile has errors")
					}
					fmt.Printf("OK: %d pattern rule(s), %d table(s), %d warning(s)\n", len(rs.Rules), len(rs.Tables), len(issues))
					return nil
				},
			},
			{
				Name:      "delete",
				Usage:     "Delete a saved profile",
				ArgsUsage: "<name>",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					store, err := storeFor()
					if err != nil {
						return err
					}
					p, err := store.Get(cmd.Args().First())
					if err != nil {
						return fmt.Errorf("%q: %w", cmd.Args().First(), err)
					}
					if _, err := store.Delete(p.ID); err != nil {
						return err
					}
					fmt.Printf("Deleted profile %q\n", p.Name())
					return nil
				},
			},
		},
	}
}
