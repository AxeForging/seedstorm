package seeder

import (
	"testing"

	"github.com/AxeForging/seedstorm/internal/schema"
)

func TestChunkSize_FixedSoMemoryDoesNotGrowWithTheTable(t *testing.T) {
	if got := chunkSize(0); got != DefaultChunkRows {
		t.Errorf("chunkSize(0) = %d, want %d", got, DefaultChunkRows)
	}
	if got := chunkSize(7); got != 7 {
		t.Errorf("chunkSize(7) = %d, want 7", got)
	}
}

func TestFinish_StatusFromMissingRows(t *testing.T) {
	cases := []struct {
		inserted, requested int64
		status              string
		missing             int64
	}{
		{10, 10, StatusOK, 0},
		{4, 10, StatusPartial, 6},
		{0, 10, StatusFailed, 10},
	}
	for _, c := range cases {
		got := finish(TableResult{Requested: c.requested, Inserted: c.inserted, Rejected: 99})
		if got.Status != c.status || got.Missing != c.missing {
			t.Errorf("finish(%d/%d) = %s missing %d, want %s missing %d", c.inserted, c.requested, got.Status, got.Missing, c.status, c.missing)
		}
	}
}

func TestRejectBudget_AllowsHeavyRejectionButStaysFinite(t *testing.T) {
	if b := rejectBudget(200, DefaultMaxRowFailures); b < 200*9 {
		t.Fatalf("budget %d cannot absorb a 90%% rejection rate for 200 rows", b)
	}
	if b := rejectBudget(0, DefaultMaxRowFailures); b <= 0 {
		t.Fatalf("budget %d must stay positive", b)
	}
}

func TestPreloadTables_TableAndItsParentsOnly(t *testing.T) {
	sc := &schema.Schema{Tables: map[string]schema.Table{
		"users": {Columns: map[string]schema.Column{"id": {PK: true}}},
		"teams": {Columns: map[string]schema.Column{"id": {PK: true}}},
		"audit": {Columns: map[string]schema.Column{"id": {PK: true}}},
		"member": {Columns: map[string]schema.Column{
			"user_id": {FK: "users.id"}, "team_id": {FK: "teams.id", Nullable: true},
			"mentor_id": {FK: "member.id"}, "ghost_id": {FK: "ghosts.id"},
		}},
	}}
	got := map[string]bool{}
	for _, t := range preloadTables(sc, "member") {
		got[t] = true
	}
	if len(got) != 3 || !got["member"] || !got["users"] || !got["teams"] {
		t.Fatalf("preload = %v, want member plus its existing parents", got)
	}
}
