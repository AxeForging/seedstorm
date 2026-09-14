package web

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/AxeForging/seedstorm/internal/ai"
	"github.com/AxeForging/seedstorm/internal/dataio"
	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/faker"
	"github.com/AxeForging/seedstorm/internal/graph"
	"github.com/AxeForging/seedstorm/internal/rules"
	"github.com/AxeForging/seedstorm/internal/schema"
	"github.com/AxeForging/seedstorm/internal/seeder"
	"github.com/goccy/go-yaml"
	"github.com/rs/zerolog"
)

// jobLogger returns a zerolog.Logger that writes structured lines into the
// job's log stream via the provided io.Writer. Runners receive a JobControl
// (which embeds io.Writer) so they can also call Phase() and Progress().
func jobLogger(w io.Writer) zerolog.Logger {
	cw := zerolog.ConsoleWriter{
		Out:          w,
		NoColor:      true,
		TimeFormat:   "15:04:05",
		PartsExclude: []string{zerolog.TimestampFieldName},
		FormatLevel: func(i interface{}) string {
			if s, ok := i.(string); ok {
				return strings.ToUpper(s)
			}
			return "INFO"
		},
	}
	return zerolog.New(cw).With().Timestamp().Logger()
}

// SeedRequest mirrors the seed CLI flags in JSON form. Tables, when set,
// restricts seeding to the listed tables plus their transitive non-nullable
// FK parents.
type SeedRequest struct {
	Rows         int            `json:"rows"`
	EnumRows     int            `json:"enumRows"`
	BatchSize    int            `json:"batchSize"`
	SelfRefDepth *int           `json:"selfRefDepth,omitempty"`
	DisableFK    bool           `json:"disableFK"`
	Truncate     bool           `json:"truncate"`
	DryRun       bool           `json:"dryRun"`
	Tables       []string       `json:"tables,omitempty"`
	TableRows    map[string]int `json:"tableRows,omitempty"`
	ProfileID    string         `json:"profileId,omitempty"`
}

type CloneSchemaRequest struct {
	TargetID      string         `json:"targetId"`
	TargetSavedID string         `json:"targetSavedId,omitempty"`
	Target        ConnectionInfo `json:"target,omitempty"`
	TargetDSN     string         `json:"targetDsn,omitempty"`
	Password      string         `json:"password,omitempty"`
	DropExisting  bool           `json:"dropExisting"`
	DryRun        bool           `json:"dryRun"`
}

func (s *Server) runCloneSchema(ctx context.Context, sess *Session, req CloneSchemaRequest, jc JobControl) (map[string]any, error) {
	log := jobLogger(jc)
	target, err := s.resolveCloneTarget(req, sess)
	if err != nil {
		return nil, err
	}
	if sess.DBType != target.DBType {
		return nil, fmt.Errorf("schema clone requires matching database types: source %q target %q", sess.Info.DBType, target.Info.DBType)
	}

	jc.Phase("introspect")
	log.Info().
		Str("source", connectionLabelForLog(sess.Info)).
		Str("target", connectionLabelForLog(target.Info)).
		Msg("Inspecting source schema")
	tables, err := sess.RawTables()
	if err != nil {
		return nil, fmt.Errorf("source introspection: %w", err)
	}
	log.Info().Int("tables", len(tables)).Msg("Source schema introspected")
	jc.Phase("plan")
	stmts, err := db.BuildSchemaDDL(tables, sess.DBType, req.DropExisting)
	if err != nil {
		return nil, err
	}
	log.Info().
		Int("tables", len(tables)).
		Int("statements", len(stmts)).
		Bool("drop_existing", req.DropExisting).
		Bool("dry_run", req.DryRun).
		Msg("Clone DDL planned")
	if req.DryRun {
		return map[string]any{
			"tables":     len(tables),
			"statements": len(stmts),
			"dryRun":     true,
			"format":     "sql",
			"output":     strings.Join(stmts, ";\n") + ";",
		}, nil
	}

	if !req.DropExisting {
		jc.Phase("target")
		log.Info().Msg("Checking target database is empty")
		existing, err := target.RawTables()
		if err != nil {
			return nil, fmt.Errorf("target inspection: %w", err)
		}
		if len(existing) > 0 {
			return nil, fmt.Errorf("target database is not empty (%d tables); enable drop existing to replace it", len(existing))
		}
		log.Info().Msg("Target database is empty")
	}
	jc.Phase("create")
	log.Info().Int("statements", len(stmts)).Msg("Executing schema DDL")
	conn, err := target.OpenRunConn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := db.ExecSchemaDDLWithProgress(ctx, conn, target.DBType, stmts, func(done, total int, label string) {
		if done == 1 || done == total || done%10 == 0 {
			jc.Progress(done, total, label)
		}
	}); err != nil {
		return nil, err
	}
	log.Info().Int("statements", len(stmts)).Msg("Schema DDL executed")
	target.SetSchema(nil)
	jc.Phase("done")
	log.Info().
		Int("tables", len(tables)).
		Int("statements", len(stmts)).
		Str("target", connectionLabelForLog(target.Info)).
		Msg("Schema clone complete")
	return map[string]any{
		"tables":     len(tables),
		"statements": len(stmts),
		"target":     target.Info.DBName,
	}, nil
}

func (s *Server) resolveCloneTarget(req CloneSchemaRequest, source *Session) (*Session, error) {
	if req.TargetSavedID != "" || req.TargetID != "" {
		if req.TargetID != "" && req.TargetID == source.ID {
			return nil, fmt.Errorf("target connection must be different from source")
		}
		return s.resolveConnection(ConnRef{ID: req.TargetID, SavedID: req.TargetSavedID}, "target")
	}
	if strings.TrimSpace(req.TargetDSN) != "" {
		driver, dsn, info, err := buildRawDSN(req.Target.DBType, req.TargetDSN, req.Target.Params)
		if err != nil {
			return nil, err
		}
		info.Label = req.Target.Label
		return s.sessions.OpenDSN(driver, dsn, info)
	}
	if req.Target.DBType == "" || req.Target.DBName == "" || req.Target.User == "" {
		return nil, fmt.Errorf("target connection is required")
	}
	return s.sessions.Open(req.Target, req.Password)
}

func (s *Server) runSeed(ctx context.Context, sess *Session, req SeedRequest, jc JobControl) (map[string]any, error) {
	log := jobLogger(jc)
	tableRows := cleanTableRows(req.TableRows)
	truncateOnly := req.Truncate && req.Rows == 0 && req.EnumRows == 0 && len(tableRows) == 0 && req.ProfileID == ""
	if req.Rows < 0 || (req.Rows == 0 && !truncateOnly) {
		req.Rows = 100
	}
	if req.BatchSize <= 0 {
		req.BatchSize = seeder.DefaultBatchSize
	}
	start := time.Now()
	jc.Phase("build")
	sc, err := sess.Schema(false)
	if err != nil {
		return nil, fmt.Errorf("schema: %w", err)
	}
	overrides, tableRows, err := s.applyProfile(req.ProfileID, sc, tableRows, log)
	if err != nil {
		return nil, err
	}

	var allSorted []string
	if req.DisableFK {
		for name := range sc.Tables {
			allSorted = append(allSorted, name)
		}
		log.Info().Msg("FK ordering disabled — using arbitrary table order")
	} else {
		log.Info().Msg("Building dependency graph")
		g := graph.Build(sc)
		allSorted, err = g.TopologicalSort()
		if err != nil {
			return nil, err
		}
	}

	// Resolve the target set: explicit selection plus transitive parents, or
	// the full sorted set if nothing was selected.
	targetTables := allSorted
	autoSelected := map[string]bool{}
	if len(req.Tables) > 0 && !req.DisableFK {
		selected := make(map[string]bool, len(req.Tables))
		for _, t := range req.Tables {
			selected[t] = true
		}
		g := graph.Build(sc)
		targetTables, autoSelected = graph.ResolveSelection(g, selected, allSorted)
		log.Info().
			Int("explicit", len(selected)).
			Int("auto", len(autoSelected)).
			Int("total", len(targetTables)).
			Msg("Selection resolved with FK closure")
	} else if len(req.Tables) > 0 && req.DisableFK {
		// FK disabled: honor selection literally, no closure.
		targetTables = req.Tables
	}
	log.Info().Str("order", strings.Join(targetTables, " → ")).Msg("Seed order resolved")

	conn := sess.Conn()
	if !req.DryRun {
		runConn, err := sess.OpenRunConn(ctx)
		if err != nil {
			return nil, err
		}
		defer runConn.Close()
		conn = runConn
	}

	if req.Truncate && !req.DryRun {
		jc.Phase("truncate")
		log.Info().Int("tables", len(targetTables)).Msg("Truncating tables")
		if err := db.TruncateWithProgress(ctx, conn, sess.DBType, targetTables, func(done, total int, table string) {
			if table != "" {
				log.Info().Str("table", table).Msg("Truncating table")
			}
			jc.Progress(done, total, table)
		}); err != nil {
			return nil, fmt.Errorf("truncate: %w", err)
		}
		log.Info().Msg("Truncate complete")
	}

	if truncateOnly {
		tableCounts := make(map[string]int, len(targetTables))
		for _, tableName := range targetTables {
			tableCounts[tableName] = 0
		}
		elapsed := time.Since(start).Round(time.Millisecond)
		jc.Phase("done")
		log.Info().
			Int("tables", len(targetTables)).
			Dur("duration", elapsed).
			Msg("Truncate-only seed run complete")
		autoList := make([]string, 0, len(autoSelected))
		for t := range autoSelected {
			autoList = append(autoList, t)
		}
		return map[string]any{
			"tables":      len(targetTables),
			"totalRows":   0,
			"durationMs":  elapsed.Milliseconds(),
			"dryRun":      req.DryRun,
			"truncated":   true,
			"order":       targetTables,
			"auto":        autoList,
			"tableCounts": tableCounts,
		}, nil
	}

	// Rows are generated and written chunk by chunk, so memory stays flat for
	// any row count.
	if req.DryRun {
		jc.Phase("generate")
	} else {
		jc.Phase("insert")
	}
	log.Info().Int("rows", req.Rows).Msg("Generating fake data")
	connArg := conn
	if req.DryRun {
		connArg = nil
	}
	warnings := make([]faker.GenerationWarning, 0)
	if !req.DryRun {
		defer syncSequencesLogged(ctx, conn, sess.DBType, targetTables, log)
	}
	dryRunSQL := &cappedSQL{limit: webOutputLimit}
	// allSorted is preloaded so target tables can FK-reference already-populated
	// parents; targetTables alone is what gets generated.
	res, err := seeder.Seed(ctx, connArg, sess.DBType, sc, allSorted, targetTables, seeder.SeedOptions{
		Rows: req.Rows, EnumRows: req.EnumRows, TableRows: tableRows, BatchSize: req.BatchSize, DryRun: req.DryRun,
		Generate: faker.GenerateOptions{
			SelfRefDepth: requestSelfRefDepth(req.SelfRefDepth),
			Overrides:    overrides,
			OnWarning:    collectWarning(&warnings, log),
		},
		OnTableStart: func(table string) error { log.Info().Str("table", table).Msg("Seeding table"); return nil },
		OnRows: func(table string, rows []map[string]interface{}) error {
			if req.DryRun {
				dryRunSQL.write(table, rows, req.BatchSize, sess.DBType)
			}
			return nil
		},
		OnTable: func(p seeder.Progress) { jc.Progress(p.TableIndex, p.Tables, p.Table) },
	})
	if err != nil {
		return nil, err
	}
	totalRows := res.Total
	tableCounts := res.Counts
	elapsed := time.Since(start).Round(time.Millisecond)
	jc.Phase("done")
	log.Info().
		Int("tables", len(targetTables)).
		Int("total_rows", totalRows).
		Dur("duration", elapsed).
		Msg("Seeding complete")
	autoList := make([]string, 0, len(autoSelected))
	for t := range autoSelected {
		autoList = append(autoList, t)
	}
	result := map[string]any{
		"tables":      len(targetTables),
		"totalRows":   totalRows,
		"durationMs":  elapsed.Milliseconds(),
		"dryRun":      req.DryRun,
		"order":       targetTables,
		"auto":        autoList,
		"tableCounts": tableCounts,
	}
	if len(warnings) > 0 {
		result["warnings"] = generationWarningsView(warnings)
	}
	if req.DryRun {
		result["output"] = dryRunSQL.String()
		result["format"] = "sql"
	}
	return result, nil
}

// GapsRequest mirrors the gaps CLI flags. Tables, when set, restricts the
// fill phase to the listed empty tables (plus their transitive parents).
type GapsRequest struct {
	Rows         int            `json:"rows"`
	EnumRows     int            `json:"enumRows"`
	BatchSize    int            `json:"batchSize"`
	SelfRefDepth *int           `json:"selfRefDepth,omitempty"`
	Fill         bool           `json:"fill"`
	DryRun       bool           `json:"dryRun"`
	Tables       []string       `json:"tables,omitempty"`
	TableRows    map[string]int `json:"tableRows,omitempty"`
	ProfileID    string         `json:"profileId,omitempty"`
}

func (s *Server) runGaps(ctx context.Context, sess *Session, req GapsRequest, jc JobControl) (map[string]any, error) {
	log := jobLogger(jc)
	if req.Rows <= 0 {
		req.Rows = 100
	}
	if req.BatchSize <= 0 {
		req.BatchSize = seeder.DefaultBatchSize
	}
	jc.Phase("build")
	sc, err := sess.Schema(false)
	if err != nil {
		return nil, err
	}
	g := graph.Build(sc)
	allSorted, err := g.TopologicalSort()
	if err != nil {
		return nil, err
	}

	conn := sess.Conn()
	if req.Fill && !req.DryRun {
		runConn, err := sess.OpenRunConn(ctx)
		if err != nil {
			return nil, err
		}
		defer runConn.Close()
		conn = runConn
	}
	jc.Phase("scan")
	log.Info().Int("tables", len(allSorted)).Msg("Scanning row counts")
	counts, err := db.GetTableRowCounts(ctx, conn, sess.DBType, allSorted)
	if err != nil {
		return nil, err
	}

	// Default gap set: every empty table, in topological order.
	var gapTables []string
	for _, t := range allSorted {
		if counts[t] == 0 {
			gapTables = append(gapTables, t)
		}
	}
	// If the caller scoped the fill, intersect with empty tables and resolve
	// non-nullable parents (which may themselves be empty).
	if len(req.Tables) > 0 {
		selected := make(map[string]bool, len(req.Tables))
		for _, t := range req.Tables {
			if counts[t] == 0 {
				selected[t] = true
			}
		}
		g := graph.Build(sc)
		resolved, _ := graph.ResolveSelection(g, selected, allSorted)
		// Keep only resolved tables that are actually empty — populated
		// parents do not need re-seeding.
		gapTables = gapTables[:0]
		for _, t := range resolved {
			if counts[t] == 0 {
				gapTables = append(gapTables, t)
			}
		}
	}
	log.Info().Int("gap_tables", len(gapTables)).Msg("Gap analysis complete")

	result := map[string]any{
		"counts":    counts,
		"gapTables": gapTables,
		"all":       allSorted,
	}
	if !req.Fill || len(gapTables) == 0 {
		jc.Phase("done")
		return result, nil
	}

	jc.Phase("generate")
	log.Info().Int("gap_tables", len(gapTables)).Int("rows", req.Rows).Msg("Generating data for empty tables")
	overrides, gapRows, err := s.applyProfile(req.ProfileID, sc, cleanTableRows(req.TableRows), log)
	if err != nil {
		return nil, err
	}
	warnings := make([]faker.GenerationWarning, 0)
	jc.Phase("insert")
	if !req.DryRun {
		defer syncSequencesLogged(ctx, conn, sess.DBType, gapTables, log)
	}
	res, err := seeder.Seed(ctx, conn, sess.DBType, sc, allSorted, gapTables, seeder.SeedOptions{
		Rows: req.Rows, EnumRows: req.EnumRows, TableRows: gapRows, BatchSize: req.BatchSize, DryRun: req.DryRun,
		Generate: faker.GenerateOptions{
			SelfRefDepth: requestSelfRefDepth(req.SelfRefDepth),
			Overrides:    overrides,
			OnWarning:    collectWarning(&warnings, log),
		},
		OnTableStart: func(table string) error { log.Info().Str("table", table).Msg("Filling table"); return nil },
		OnTable:      func(p seeder.Progress) { jc.Progress(p.TableIndex, p.Tables, p.Table) },
	})
	if err != nil {
		return nil, err
	}
	totalRows := res.Total
	result["filled"] = totalRows
	if len(warnings) > 0 {
		result["warnings"] = generationWarningsView(warnings)
	}
	jc.Phase("done")
	log.Info().Int("filled_rows", totalRows).Msg("Gap fill complete")
	return result, nil
}

// GenerateRequest mirrors the generate CLI flags. Tables, when set, restricts
// generation to the listed tables plus their transitive non-nullable parents.
type GenerateRequest struct {
	Rows         int            `json:"rows"`
	SelfRefDepth *int           `json:"selfRefDepth,omitempty"`
	Format       string         `json:"format"` // yaml | json | sql
	Tables       []string       `json:"tables,omitempty"`
	TableRows    map[string]int `json:"tableRows,omitempty"`
	ProfileID    string         `json:"profileId,omitempty"`
}

func (s *Server) runGenerate(ctx context.Context, sess *Session, req GenerateRequest, jc JobControl) (map[string]any, error) {
	log := jobLogger(jc)
	if req.Rows <= 0 {
		req.Rows = 10
	}
	jc.Phase("resolve")
	sc, err := sess.Schema(false)
	if err != nil {
		return nil, err
	}
	g := graph.Build(sc)
	allSorted, err := g.TopologicalSort()
	if err != nil {
		return nil, err
	}

	targetTables := allSorted
	if len(req.Tables) > 0 {
		selected := make(map[string]bool, len(req.Tables))
		for _, t := range req.Tables {
			selected[t] = true
		}
		var auto map[string]bool
		targetTables, auto = graph.ResolveSelection(g, selected, allSorted)
		log.Info().Int("explicit", len(selected)).Int("auto", len(auto)).Int("total", len(targetTables)).Msg("Selection resolved")
	}

	jc.Phase("generate")
	log.Info().Int("rows", req.Rows).Int("tables", len(targetTables)).Msg("Generating fake data")
	overrides, genRows, err := s.applyProfile(req.ProfileID, sc, cleanTableRows(req.TableRows), log)
	if err != nil {
		return nil, err
	}
	warnings := make([]faker.GenerationWarning, 0)
	// The document streams into a bounded buffer: the browser gets a complete
	// file or a clear refusal, never an unbounded or truncated one.
	out := &limitedBuffer{limit: webOutputLimit}
	w, err := dataio.NewWriter(out, req.Format, sess.DBType, 1)
	if err != nil {
		return nil, err
	}
	res, err := seeder.Seed(ctx, nil, sess.DBType, sc, allSorted, targetTables, seeder.SeedOptions{
		Rows: req.Rows, TableRows: genRows, DryRun: true,
		Generate: faker.GenerateOptions{
			SelfRefDepth: requestSelfRefDepth(req.SelfRefDepth),
			Overrides:    overrides,
			OnWarning:    collectWarning(&warnings, log),
		},
		OnTableStart: w.Table,
		OnRows:       func(_ string, rows []map[string]interface{}) error { return w.Rows(rows) },
	})
	if err == nil {
		err = w.Close()
	}
	if err != nil {
		return nil, err
	}
	jc.Phase("done")
	log.Info().Int("tables", len(targetTables)).Str("format", req.Format).Msg("Generation complete")
	result := map[string]any{
		"output":      out.String(),
		"format":      req.Format,
		"tables":      targetTables,
		"tableCounts": res.Counts,
		"totalRows":   res.Total,
	}
	if len(warnings) > 0 {
		result["warnings"] = generationWarningsView(warnings)
	}
	return result, nil
}

// syncSequencesLogged advances Postgres sequences past inserted ids and reports
// it in the job log.
func syncSequencesLogged(ctx context.Context, conn *sql.DB, dbType string, tables []string, log zerolog.Logger) {
	adjusted, err := db.SyncSequences(ctx, conn, dbType, tables)
	for _, a := range adjusted {
		log.Info().Str("table", a.Table).Str("column", a.Column).Int64("from", a.From).Int64("to", a.To).Msg("Advanced sequence past seeded ids")
	}
	if err != nil {
		log.Warn().Err(err).Msg("Could not advance sequences; application inserts may reuse seeded ids")
	}
}

// applyProfile compiles a saved profile for a run and merges its per-table row
// counts under the explicit ones. No profile id is a no-op.
func (s *Server) applyProfile(id string, sc *schema.Schema, tableRows map[string]int, log zerolog.Logger) (faker.Overrides, map[string]int, error) {
	rs, err := s.profileByID(id)
	if err != nil || rs == nil {
		return nil, tableRows, err
	}
	for _, issue := range rs.Validate(sc) {
		if issue.Severity == rules.SeverityWarning {
			log.Warn().Str("path", issue.Path).Msg(issue.Message)
		}
	}
	runID := rules.NewRunID()
	overrides, err := rs.Compile(sc, runID)
	if err != nil {
		return nil, nil, err
	}
	log.Info().Str("profile", rs.Name).Str("run", runID).Msg("Seed profile applied")
	return overrides, cleanTableRows(rules.MergeTableRowsFor(rs, sc, tableRows)), nil
}

func generationWarningsView(warnings []faker.GenerationWarning) []map[string]any {
	out := make([]map[string]any, 0, len(warnings))
	for _, w := range warnings {
		out = append(out, map[string]any{
			"table":     w.Table,
			"requested": w.Requested,
			"generated": w.Generated,
			"reason":    w.Reason,
		})
	}
	return out
}

func cleanTableRows(rows map[string]int) map[string]int {
	if len(rows) == 0 {
		return nil
	}
	clean := make(map[string]int, len(rows))
	for tableName, count := range rows {
		if tableName != "" && count > 0 {
			clean[tableName] = count
		}
	}
	if len(clean) == 0 {
		return nil
	}
	return clean
}

func requestSelfRefDepth(depth *int) int {
	if depth == nil {
		return faker.DefaultSelfRefDepth
	}
	return *depth
}

func connectionLabelForLog(info ConnectionInfo) string {
	name := info.Label
	if name == "" {
		name = info.DBName
	}
	if name == "" {
		name = "database"
	}
	host := info.Host
	if host == "" {
		host = "connection"
	}
	if info.Port > 0 {
		return fmt.Sprintf("%s @ %s:%d", name, host, info.Port)
	}
	return fmt.Sprintf("%s @ %s", name, host)
}

// EnrichRequest mirrors the enrich CLI flags.
type EnrichRequest struct {
	Model   string `json:"model"`
	Context string `json:"context"`
}

func (s *Server) runEnrich(ctx context.Context, sess *Session, req EnrichRequest, jc JobControl) (map[string]any, error) {
	log := jobLogger(jc)
	model := req.Model
	if model == "" {
		model = "gemini-2.5-flash"
	}
	jc.Phase("schema")
	sc, err := sess.Schema(false)
	if err != nil {
		return nil, err
	}
	jc.Phase("ai-call")
	log.Info().Str("model", model).Int("tables", len(sc.Tables)).Msg("Enriching faker mappings")
	enriched, usedModel, err := ai.EnrichFakerMappings(ctx, sc, model, req.Context)
	if err != nil {
		return nil, err
	}
	jc.Phase("apply")
	sess.SetSchema(enriched)
	out, err := yaml.Marshal(enriched)
	if err != nil {
		return nil, err
	}
	jc.Phase("done")
	log.Info().Str("model", usedModel).Msg("Enrichment complete (cached schema updated)")
	return map[string]any{
		"model":  usedModel,
		"yaml":   string(out),
		"tables": len(enriched.Tables),
	}, nil
}

// ExportRequest mirrors the export CLI flags. The data YAML is supplied inline
// (the UI lets users pipe a Generate run into Export without touching disk).
type ExportRequest struct {
	DataYAML  string `json:"dataYaml"`
	Format    string `json:"format"`
	BatchSize int    `json:"batchSize"`
}

func (s *Server) runExport(_ context.Context, sess *Session, req ExportRequest, jc JobControl) (map[string]any, error) {
	log := jobLogger(jc)
	if req.BatchSize <= 0 {
		req.BatchSize = 100
	}
	jc.Phase("format")
	out := &limitedBuffer{limit: webOutputLimit}
	w, err := dataio.NewWriter(out, req.Format, sess.DBType, req.BatchSize)
	if err != nil {
		return nil, err
	}
	tableCounts := map[string]int{}
	totalRows := 0
	err = dataio.ReadTables(strings.NewReader(req.DataYAML), 0, func(table string, rows []map[string]interface{}) error {
		if rows == nil {
			tableCounts[table] = 0
			return w.Table(table)
		}
		tableCounts[table] += len(rows)
		totalRows += len(rows)
		return w.Rows(rows)
	})
	if err == nil {
		err = w.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("export: %w", err)
	}
	jc.Phase("done")
	log.Info().Str("format", req.Format).Int("tables", len(tableCounts)).Msg("Export complete")
	return map[string]any{
		"output":      out.String(),
		"format":      req.Format,
		"tables":      len(tableCounts),
		"tableCounts": tableCounts,
		"totalRows":   totalRows,
	}, nil
}

// collectWarning records generation warnings for the job result and logs them.
func collectWarning(warnings *[]faker.GenerationWarning, log zerolog.Logger) func(faker.GenerationWarning) {
	return func(w faker.GenerationWarning) {
		*warnings = append(*warnings, w)
		log.Warn().
			Str("table", w.Table).
			Int("requested", w.Requested).
			Int("generated", w.Generated).
			Str("reason", w.Reason).
			Msg("Generation volume capped")
	}
}

// webOutputLimit caps text a web job returns to the browser. A dry run's SQL
// is cut there with a note; generate and export refuse instead, since a cut
// document would be unusable.
var webOutputLimit = 20 << 20

// errOutputTooLarge explains how to produce a document the browser cannot hold.
var errOutputTooLarge = fmt.Errorf("output is larger than %dMB; write it to a file with the CLI (seedstorm generate --out, seedstorm export --out)", webOutputLimit>>20)

// limitedBuffer collects output up to limit bytes and fails past it.
type limitedBuffer struct {
	strings.Builder
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, errOutputTooLarge
	}
	return b.Builder.Write(p)
}

// cappedSQL collects dry-run INSERTs up to limit bytes and counts the rows it
// had to leave out.
type cappedSQL struct {
	sb      strings.Builder
	limit   int
	omitted int
}

func (c *cappedSQL) write(table string, rows []map[string]interface{}, batchSize int, dbType string) {
	for _, batch := range db.SplitBatches(rows, batchSize) {
		if c.omitted > 0 || c.sb.Len() >= c.limit {
			c.omitted += len(batch)
			continue
		}
		c.sb.WriteString(db.RenderInsert(table, batch, dbType))
		c.sb.WriteString("\n")
	}
}

func (c *cappedSQL) String() string {
	if c.omitted == 0 {
		return c.sb.String()
	}
	return c.sb.String() + fmt.Sprintf("-- %d more rows not shown: dry-run output stops at %dMB\n", c.omitted, c.limit>>20)
}
