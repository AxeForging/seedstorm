package web

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/AxeForging/seedstorm/internal/ai"
	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/faker"
	"github.com/AxeForging/seedstorm/internal/graph"
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
}

func (s *Server) runSeed(ctx context.Context, sess *Session, req SeedRequest, jc JobControl) (map[string]any, error) {
	log := jobLogger(jc)
	if req.Rows <= 0 {
		req.Rows = 100
	}
	if req.BatchSize <= 0 {
		req.BatchSize = 100
	}
	jc.Phase("build")
	sc, err := sess.Schema(false)
	if err != nil {
		return nil, fmt.Errorf("schema: %w", err)
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

	if req.Truncate && !req.DryRun {
		jc.Phase("truncate")
		log.Info().Int("tables", len(targetTables)).Msg("Truncating tables")
		if err := db.Truncate(ctx, conn, sess.DBType, targetTables); err != nil {
			return nil, fmt.Errorf("truncate: %w", err)
		}
		log.Info().Msg("Truncate complete")
	}

	start := time.Now()
	jc.Phase("generate")
	log.Info().Int("rows", req.Rows).Msg("Generating fake data")
	connArg := conn
	if req.DryRun {
		connArg = nil
	}
	// GenerateFiltered preloads PKs from allSorted so target tables can FK-ref
	// already-populated parents; targetTables alone is what gets generated.
	data, err := faker.GenerateFilteredWithOptions(sc, allSorted, targetTables, req.Rows, req.EnumRows, cleanTableRows(req.TableRows), connArg, sess.DBType, faker.GenerateOptions{
		SelfRefDepth: requestSelfRefDepth(req.SelfRefDepth),
	})
	if err != nil {
		return nil, fmt.Errorf("generation: %w", err)
	}

	jc.Phase("insert")
	totalRows := 0
	tableCounts := make(map[string]int, len(targetTables))
	var dryRunSQL strings.Builder
	for idx, tableName := range targetTables {
		tableRows := data[tableName]
		tableCounts[tableName] = len(tableRows)
		log.Info().Str("table", tableName).Int("rows", len(tableRows)).Msg("Seeding table")
		for i := 0; i < len(tableRows); i += req.BatchSize {
			end := i + req.BatchSize
			if end > len(tableRows) {
				end = len(tableRows)
			}
			if req.DryRun {
				query, _ := db.BuildBatchInsert(tableName, tableRows[i:end], sess.DBType)
				dryRunSQL.WriteString(query)
				dryRunSQL.WriteString(";\n")
			} else {
				query, values := db.BuildBatchInsert(tableName, tableRows[i:end], sess.DBType)
				if _, err := conn.ExecContext(ctx, query, values...); err != nil {
					return nil, fmt.Errorf("insert into %s: %w", tableName, err)
				}
			}
		}
		totalRows += len(tableRows)
		jc.Progress(idx+1, len(targetTables), tableName)
	}
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
}

func (s *Server) runGaps(ctx context.Context, sess *Session, req GapsRequest, jc JobControl) (map[string]any, error) {
	log := jobLogger(jc)
	if req.Rows <= 0 {
		req.Rows = 100
	}
	if req.BatchSize <= 0 {
		req.BatchSize = 100
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
	data, err := faker.GenerateFilteredWithOptions(sc, allSorted, gapTables, req.Rows, req.EnumRows, cleanTableRows(req.TableRows), conn, sess.DBType, faker.GenerateOptions{
		SelfRefDepth: requestSelfRefDepth(req.SelfRefDepth),
	})
	if err != nil {
		return nil, err
	}

	jc.Phase("insert")
	totalRows := 0
	for idx, tableName := range gapTables {
		tableRows := data[tableName]
		log.Info().Str("table", tableName).Int("rows", len(tableRows)).Msg("Filling table")
		if !req.DryRun {
			for i := 0; i < len(tableRows); i += req.BatchSize {
				end := i + req.BatchSize
				if end > len(tableRows) {
					end = len(tableRows)
				}
				query, values := db.BuildBatchInsert(tableName, tableRows[i:end], sess.DBType)
				if _, err := conn.ExecContext(ctx, query, values...); err != nil {
					return nil, fmt.Errorf("insert into %s: %w", tableName, err)
				}
			}
		}
		totalRows += len(tableRows)
		jc.Progress(idx+1, len(gapTables), tableName)
	}
	result["filled"] = totalRows
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
}

func (s *Server) runGenerate(ctx context.Context, sess *Session, req GenerateRequest, jc JobControl) (map[string]any, error) {
	_ = ctx
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
	// GenerateFiltered is fine here too: with conn=nil it skips PK preload.
	data, err := faker.GenerateFilteredWithOptions(sc, allSorted, targetTables, req.Rows, 0, cleanTableRows(req.TableRows), nil, sess.DBType, faker.GenerateOptions{
		SelfRefDepth: requestSelfRefDepth(req.SelfRefDepth),
	})
	if err != nil {
		return nil, err
	}
	jc.Phase("encode")
	output, err := encodeData(data, targetTables, req.Format, sess.DBType)
	if err != nil {
		return nil, err
	}
	tableCounts, totalRows := tableRowCounts(data, targetTables)
	jc.Phase("done")
	log.Info().Int("tables", len(targetTables)).Str("format", req.Format).Msg("Generation complete")
	return map[string]any{
		"output":      output,
		"format":      req.Format,
		"tables":      targetTables,
		"tableCounts": tableCounts,
		"totalRows":   totalRows,
	}, nil
}

func tableRowCounts(data map[string][]map[string]any, sortedTables []string) (map[string]int, int) {
	counts := make(map[string]int, len(sortedTables))
	total := 0
	for _, tableName := range sortedTables {
		n := len(data[tableName])
		counts[tableName] = n
		total += n
	}
	return counts, total
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

func encodeData(data map[string][]map[string]any, sortedTables []string, format, dbType string) (string, error) {
	switch strings.ToLower(format) {
	case "json":
		b, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			return "", err
		}
		return string(b), nil
	case "sql":
		var sb strings.Builder
		for _, tableName := range sortedTables {
			for _, row := range data[tableName] {
				query, _ := db.BuildInsert(tableName, row, dbType)
				sb.WriteString(query)
				sb.WriteString(";\n")
			}
		}
		return sb.String(), nil
	default:
		b, err := yaml.Marshal(data)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
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
	jc.Phase("parse")
	var data map[string][]map[string]any
	if err := yaml.Unmarshal([]byte(req.DataYAML), &data); err != nil {
		return nil, fmt.Errorf("parse data yaml: %w", err)
	}
	jc.Phase("format")
	dbType := sess.DBType
	var output string
	switch strings.ToLower(req.Format) {
	case "json":
		b, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			return nil, err
		}
		output = string(b)
	case "csv":
		var sb strings.Builder
		cw := csv.NewWriter(&sb)
		for tableName, rows := range data {
			if len(rows) == 0 {
				continue
			}
			headers := []string{"_table"}
			for k := range rows[0] {
				headers = append(headers, k)
			}
			_ = cw.Write(headers)
			for _, row := range rows {
				record := []string{tableName}
				for _, k := range headers[1:] {
					record = append(record, fmt.Sprintf("%v", row[k]))
				}
				_ = cw.Write(record)
			}
		}
		cw.Flush()
		if err := cw.Error(); err != nil {
			return nil, err
		}
		output = sb.String()
	default:
		var sb strings.Builder
		for tableName, rows := range data {
			for i := 0; i < len(rows); i += req.BatchSize {
				end := i + req.BatchSize
				if end > len(rows) {
					end = len(rows)
				}
				query, _ := db.BuildBatchInsert(tableName, rows[i:end], dbType)
				sb.WriteString(query)
				sb.WriteString(";\n")
			}
		}
		output = sb.String()
	}
	jc.Phase("done")
	tableNames := make([]string, 0, len(data))
	for tableName := range data {
		tableNames = append(tableNames, tableName)
	}
	tableCounts, totalRows := tableRowCounts(data, tableNames)
	log.Info().Str("format", req.Format).Int("tables", len(data)).Msg("Export complete")
	return map[string]any{
		"output":      output,
		"format":      req.Format,
		"tables":      len(data),
		"tableCounts": tableCounts,
		"totalRows":   totalRows,
	}, nil
}
