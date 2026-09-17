package web

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/AxeForging/seedstorm/internal/compare"
	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/relations"
)

// A scan fills the cache edge by edge; a newer scan or a write makes an older
// scan's late results disappear instead of mixing into the new ones.
func TestShapeCache_StaleScansAndWritesDoNotLeak(t *testing.T) {
	sess := &Session{ID: "s"}
	old := sess.shapes.begin(false)
	sess.shapes.put(old, 2, relations.Shape{Child: "orders", Column: "user_id", Outcome: db.OutcomeOK})
	if v := sess.shapes.view(); len(v.Shapes) != 1 || !v.Running || v.Total != 2 || v.TakenAt == "" {
		t.Fatalf("partial view = %+v", v)
	}

	fresh := sess.shapes.begin(true)
	sess.shapes.put(old, 2, relations.Shape{Child: "stale", Column: "x_id"})
	sess.shapes.finish(old)
	if v := sess.shapes.view(); len(v.Shapes) != 0 || !v.Running || !v.Estimate {
		t.Fatalf("a superseded scan leaked into the new one: %+v", v)
	}
	sess.shapes.put(fresh, 1, relations.Shape{Child: "orders", Column: "user_id", Outcome: relations.OutcomeEstimated})
	sess.shapes.finish(fresh)
	if v := sess.shapes.view(); len(v.Shapes) != 1 || v.Running {
		t.Fatalf("finished view = %+v", v)
	}

	sess.InvalidateCounts()
	sess.shapes.put(fresh, 1, relations.Shape{Child: "late", Column: "x_id"})
	if v := sess.shapes.view(); len(v.Shapes) != 0 || v.TakenAt != "" {
		t.Fatalf("shapes survived a write: %+v", v)
	}
}

// Production connections read estimates one query at a time unless an exact
// scan is confirmed; other connections get what was asked.
func TestRelationshipOptions_ProductionReadsEstimatesUnlessConfirmed(t *testing.T) {
	s, _, prod := productionServer(t)
	opts, notes, err := s.relationshipOptions(sessionTarget(prod), "exact", false, false)
	if err != nil || opts.Mode != relations.Estimate || opts.Limits.Concurrency != 1 || len(notes) != 1 || !strings.Contains(notes[0], "billing-prod") {
		t.Fatalf("production unconfirmed = %+v %v %v", opts, notes, err)
	}
	opts, notes, _ = s.relationshipOptions(sessionTarget(prod), "exact", true, true)
	if opts.Mode != relations.Exact || opts.Limits.Concurrency != 1 || !opts.IncludeUnindexed || len(notes) != 0 {
		t.Fatalf("production confirmed = %+v %v", opts, notes)
	}
	other := sessionTarget(&Session{Info: ConnectionInfo{DBType: "postgres", Host: "localhost", DBName: "scratch"}})
	opts, _, _ = s.relationshipOptions(other, "exact", false, false)
	if opts.Mode != relations.Exact || opts.Limits.Concurrency != 0 {
		t.Fatalf("ordinary connection = %+v", opts)
	}
	if _, _, err := s.relationshipOptions(other, "sideways", false, false); err == nil {
		t.Fatal("an unknown mode was accepted")
	}
}

// Exported files carry relationships only when asked, from a comparison or a
// snapshot; exporting never scans.
func TestSnapshotsAPI_RelationshipsOnlyWhenAsked(t *testing.T) {
	s := profileServer(t)
	s.sessions.sessions["sess-snap"] = &Session{ID: "sess-snap", DBType: "pgx"}
	shape := func(max int64) *relations.Shape {
		return &relations.Shape{Child: "orders", Column: "user_id", Parent: "users", ParentColumn: "id", Avg: 2, Max: max, Outcome: db.OutcomeOK}
	}
	report := compare.Diff(
		compare.Snapshot{Label: "prod", DBType: "pgx", Tables: map[string]compare.TableStat{"orders": {Rows: 10}}},
		compare.Snapshot{Label: "stage", DBType: "pgx", Tables: map[string]compare.TableStat{"orders": {Rows: 1}}},
	)
	report.Relationships = []compare.ShapeDrift{{Child: "orders", Column: "user_id", Status: compare.ShapeDiffers, Source: shape(9), Target: shape(1)}}

	rec, out := snapshotCall(t, s, "/api/snapshots/encode", map[string]any{"report": report, "side": "target", "format": "yaml", "relationships": true})
	if rec.Code != http.StatusOK || out["relationships"].(float64) != 1 || !strings.Contains(out["content"].(string), "max: 1") {
		t.Fatalf("target export with relationships = %d %v", rec.Code, out)
	}
	rec, parsed := snapshotCall(t, s, "/api/snapshots/parse", map[string]any{"data": out["content"]})
	if rec.Code != http.StatusOK {
		t.Fatalf("exported shapes do not parse back: %s", rec.Body)
	}
	if snap := parsed["snapshot"].(map[string]any); len(snap["relationships"].([]any)) != 1 {
		t.Fatalf("parsed = %v", parsed)
	}

	rec, out = snapshotCall(t, s, "/api/snapshots/encode", map[string]any{"report": report, "side": "source", "format": "yaml"})
	if rec.Code != http.StatusOK || out["relationships"].(float64) != 0 || strings.Contains(out["content"].(string), "relationships") {
		t.Fatalf("counts-only export = %d %v", rec.Code, out)
	}

	snap := compare.Snapshot{Label: "prod", DBType: "pgx", TakenAt: time.Now().UTC(), Tables: map[string]compare.TableStat{"orders": {Rows: 10}}, Relationships: []relations.Shape{*shape(9)}}
	_, out = snapshotCall(t, s, "/api/snapshots/encode", map[string]any{"snapshot": snap, "format": "json"})
	if strings.Contains(out["content"].(string), "relationships") {
		t.Fatalf("snapshot export without the flag kept shapes: %v", out["content"])
	}
}
