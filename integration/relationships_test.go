//go:build integration

package integration_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AxeForging/seedstorm/internal/compare"
	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/faker"
	"github.com/AxeForging/seedstorm/internal/relations"
)

// shapeSchema: 5 teams; players per team 0, 1, 1, 3 and 10 (15 players), plus
// 2 free agents with no team (NULL). Every player has exactly 2 badges except
// one with none. badges.player_id has no index on Postgres.
const shapeSchema = `
	CREATE TABLE teams (id INT PRIMARY KEY, name VARCHAR(20));
	CREATE TABLE players (id INT PRIMARY KEY, team_id INT, FOREIGN KEY (team_id) REFERENCES teams (id));
	CREATE INDEX players_team ON players (team_id);
	CREATE TABLE badges (id INT PRIMARY KEY, player_id INT NOT NULL, FOREIGN KEY (player_id) REFERENCES players (id))`

// fillShapeData inserts the rows shapeSchema describes.
func fillShapeData(t *testing.T, conn *sql.DB) {
	t.Helper()
	execSQL(t, conn, `INSERT INTO teams (id, name) VALUES (1,'a'),(2,'b'),(3,'c'),(4,'d'),(5,'e')`)
	teamOf := []int{2, 3, 4, 4, 4}
	for i := 0; i < 10; i++ {
		teamOf = append(teamOf, 5)
	}
	id := 1
	for _, team := range teamOf {
		execSQL(t, conn, fmt.Sprintf("INSERT INTO players (id, team_id) VALUES (%d, %d)", id, team))
		id++
	}
	execSQL(t, conn, fmt.Sprintf("INSERT INTO players (id, team_id) VALUES (%d, NULL); INSERT INTO players (id, team_id) VALUES (%d, NULL)", id, id+1))
	badge := 1
	for p := 1; p <= 16; p++ { // player 17 has no badges
		for k := 0; k < 2; k++ {
			execSQL(t, conn, fmt.Sprintf("INSERT INTO badges (id, player_id) VALUES (%d, %d)", badge, p))
			badge++
		}
	}
}

func findShape(t *testing.T, shapes []relations.Shape, child, column string) relations.Shape {
	t.Helper()
	for _, s := range shapes {
		if s.Child == child && s.Column == column {
			return s
		}
	}
	t.Fatalf("no shape for %s.%s in %+v", child, column, shapes)
	return relations.Shape{}
}

func TestRelationships_ExactShapesMatchTheData(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			_, conn := e.scratchDB(t, "ss_relations")
			execSQL(t, conn, shapeSchema)
			fillShapeData(t, conn)

			tables, err := db.IntrospectConn(context.Background(), conn, e.driver, nil)
			if err != nil {
				t.Fatal(err)
			}
			sc := faker.BuildSchema(e.driver, tables)
			res, err := relations.Scan(context.Background(), conn, e.driver, sc, relations.Options{Mode: relations.Exact, IncludeUnindexed: true})
			if err != nil {
				t.Fatal(err)
			}

			teams := findShape(t, res, "players", "team_id")
			if teams.Outcome != db.OutcomeOK || teams.Parents != 5 || teams.Children != 15 || teams.NullRows != 2 {
				t.Fatalf("players.team_id = %+v", teams)
			}
			if teams.Min != 1 || teams.Max != 10 || math.Abs(teams.Avg-3.75) > 0.001 || math.Abs(teams.ZeroShare-0.2) > 0.001 {
				t.Fatalf("players.team_id degrees = %+v", teams)
			}
			if math.Abs(teams.NullShare-2.0/17.0) > 0.001 || teams.P95 != 10 || teams.P50 < 1 || teams.P50 > 3 {
				t.Fatalf("players.team_id shares/percentiles = %+v", teams)
			}
			if sum := histogramParents(teams); sum != 4 {
				t.Fatalf("histogram counts %d parents with children, want 4: %+v", sum, teams.Histogram)
			}

			badges := findShape(t, res, "badges", "player_id")
			if badges.Min != 2 || badges.Max != 2 || badges.Avg != 2 || math.Abs(badges.ZeroShare-1.0/17.0) > 0.001 || badges.NullRows != 0 {
				t.Fatalf("badges.player_id = %+v", badges)
			}
		})
	}
}

func histogramParents(s relations.Shape) int64 {
	var n int64
	for _, b := range s.Histogram {
		n += b.Parents
	}
	return n
}

// Exact scans of an unindexed foreign key are full table scans: skipped by
// default with the reason, and an estimate where the database has one.
func TestRelationships_UnindexedKeyIsSkippedByDefault(t *testing.T) {
	e := postgresEngine()
	_, conn := e.scratchDB(t, "ss_relations_gate")
	execSQL(t, conn, shapeSchema)
	tables, err := db.IntrospectConn(context.Background(), conn, e.driver, nil)
	if err != nil {
		t.Fatal(err)
	}
	sc := faker.BuildSchema(e.driver, tables)
	res, err := relations.Scan(context.Background(), conn, e.driver, sc, relations.Options{Mode: relations.Exact})
	if err != nil {
		t.Fatal(err)
	}
	if s := findShape(t, res, "badges", "player_id"); s.Outcome != relations.OutcomeSkippedUnindexed {
		t.Fatalf("unindexed badges.player_id = %+v", s)
	}
	if s := findShape(t, res, "players", "team_id"); s.Outcome != db.OutcomeOK {
		t.Fatalf("indexed players.team_id = %+v", s)
	}
}

// A statement timeout ends one relationship, not the scan; cancelling keeps
// the relationships already measured.
func TestRelationships_TimeoutAndCancelKeepFinishedEdges(t *testing.T) {
	e := postgresEngine()
	_, conn := e.scratchDB(t, "ss_relations_limits")
	execSQL(t, conn, `
		CREATE TABLE parents (id INT PRIMARY KEY);
		CREATE TABLE small_kids (id INT PRIMARY KEY, parent_id INT NOT NULL REFERENCES parents (id));
		CREATE INDEX small_kids_parent ON small_kids (parent_id);
		CREATE TABLE big_kids (id INT PRIMARY KEY, parent_id INT NOT NULL REFERENCES parents (id));
		CREATE INDEX big_kids_parent ON big_kids (parent_id);
		INSERT INTO parents SELECT g FROM generate_series(1, 2000) g;
		INSERT INTO small_kids SELECT g, 1 + g % 2000 FROM generate_series(1, 100) g;
		INSERT INTO big_kids SELECT g, 1 + g % 2000 FROM generate_series(1, 3000000) g;
		ANALYZE`)
	tables, err := db.IntrospectConn(context.Background(), conn, e.driver, nil)
	if err != nil {
		t.Fatal(err)
	}
	sc := faker.BuildSchema(e.driver, tables)
	res, err := relations.Scan(context.Background(), conn, e.driver, sc, relations.Options{
		Mode: relations.Exact, Limits: db.ReadLimits{StatementTimeout: 30 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	if s := findShape(t, res, "big_kids", "parent_id"); s.Outcome != db.OutcomeTimedOut {
		t.Fatalf("3M-row edge with a 30ms limit = %+v", s)
	}
	if s := findShape(t, res, "small_kids", "parent_id"); s.Outcome != db.OutcomeOK {
		t.Fatalf("small edge = %+v", s)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var seen []relations.Shape
	res, _ = relations.Scan(ctx, conn, e.driver, sc, relations.Options{
		Mode: relations.Exact,
		OnEdge: func(done, total int, s relations.Shape) {
			seen = append(seen, s)
			if s.Child == "small_kids" {
				cancel()
			}
		},
	})
	if s := findShape(t, res, "small_kids", "parent_id"); s.Outcome != db.OutcomeOK {
		t.Fatalf("finished edge lost after cancel: %+v", s)
	}
	if s := findShape(t, res, "big_kids", "parent_id"); s.Outcome != db.OutcomeCancelled {
		t.Fatalf("edge after cancel = %+v", s)
	}
}

// TestRelationships_BinarySnapshotAndCompare exports shapes with the binary,
// compares them against a database shaped differently, and refuses a counts
// file without shapes naming the side.
func TestRelationships_BinarySnapshotAndCompare(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			srcDSN, src := e.scratchDB(t, "ss_relations_src")
			execSQL(t, src, shapeSchema)
			fillShapeData(t, src)
			tgtDSN, tgt := e.scratchDB(t, "ss_relations_tgt")
			execSQL(t, tgt, shapeSchema)
			execSQL(t, tgt, `INSERT INTO teams (id, name) VALUES (1,'a'),(2,'b')`)
			execSQL(t, tgt, `INSERT INTO players (id, team_id) VALUES (1, 1)`)
			execSQL(t, tgt, `INSERT INTO players (id, team_id) VALUES (2, 2)`)
			dir := t.TempDir()

			snapPath := filepath.Join(dir, "source.yaml")
			_, stderr, err := runBinResult(t, "snapshot", "--db", e.name, "--dsn", srcDSN, "--relationships", "--out", snapPath)
			if err != nil {
				t.Fatalf("snapshot --relationships: %v\n%s", err, stderr)
			}
			if !strings.Contains(stderr, "Relationships measured") {
				t.Errorf("no summary line:\n%s", stderr)
			}
			snap := readSnapshotFile(t, snapPath)
			teams := findShape(t, snap.Relationships, "players", "team_id")
			if teams.Outcome != db.OutcomeOK || teams.Max != 10 || teams.Parents != 5 {
				t.Fatalf("exported players.team_id = %+v", teams)
			}
			badges := findShape(t, snap.Relationships, "badges", "player_id")
			if e.name == "postgres" {
				if badges.Outcome != relations.OutcomeSkippedUnindexed || !strings.Contains(stderr, "leads no index") {
					t.Fatalf("unindexed badges.player_id = %+v\n%s", badges, stderr)
				}
			} else if badges.Outcome != db.OutcomeOK || badges.Max != 2 {
				t.Fatalf("badges.player_id = %+v", badges)
			}

			relPath := filepath.Join(dir, "relationships.json")
			runBin(t, "introspect", "--db", e.name, "--dsn", srcDSN, "--out", filepath.Join(dir, "schema.yaml"), "--relationships", relPath, "--scan-unindexed")
			fromIntrospect := readSnapshotFile(t, relPath)
			if b := findShape(t, fromIntrospect.Relationships, "badges", "player_id"); b.Outcome != db.OutcomeOK || b.Max != 2 {
				t.Fatalf("introspect --scan-unindexed badges.player_id = %+v", b)
			}

			out := runBin(t, "compare", "--source-snapshot", snapPath, "--target-db", e.name, "--target-dsn", tgtDSN, "--relationships", "--format", "json")
			var report compare.Report
			if err := json.Unmarshal([]byte(out), &report); err != nil {
				t.Fatalf("compare json: %v\n%s", err, out)
			}
			drift := map[string]compare.ShapeDrift{}
			for _, d := range report.Relationships {
				drift[d.Child+"."+d.Column] = d
			}
			if d := drift["players.team_id"]; d.Status != compare.ShapeDiffers || d.MaxDelta != -9 {
				t.Fatalf("players.team_id drift = %+v", d)
			}

			table := runBin(t, "compare", "--source-snapshot", snapPath, "--target-db", e.name, "--target-dsn", tgtDSN, "--relationships", "--only-diff")
			if !strings.Contains(table, "Relationships (children per parent") || !strings.Contains(table, "players.team_id → teams") {
				t.Fatalf("table output lacks relationships:\n%s", table)
			}

			countsOnly := filepath.Join(dir, "counts.yaml")
			runBin(t, "snapshot", "--db", e.name, "--dsn", srcDSN, "--out", countsOnly)
			_, stderr, err = runBinResult(t, "compare", "--source-snapshot", countsOnly, "--target-db", e.name, "--target-dsn", tgtDSN, "--relationships")
			if err == nil || !strings.Contains(stderr, "source") || !strings.Contains(stderr, "no relationships") {
				t.Fatalf("counts-only snapshot with --relationships: err=%v\n%s", err, stderr)
			}
			if data, _ := os.ReadFile(countsOnly); strings.Contains(string(data), "relationships") {
				t.Fatalf("counts-only file mentions relationships:\n%s", data)
			}
		})
	}
}
