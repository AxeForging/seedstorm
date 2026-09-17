package compare

import (
	"math"
	"strings"
	"testing"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/relations"
)

func TestDiffShapes_MatchesRelationshipsAndMeasuresDrift(t *testing.T) {
	source := []relations.Shape{
		{Child: "players", Column: "team_id", Parent: "teams", Avg: 3.75, P95: 10, Max: 10, ZeroShare: 0.2, Outcome: db.OutcomeOK},
		{Child: "badges", Column: "player_id", Parent: "players", Avg: 2, P95: 2, Max: 2, Outcome: db.OutcomeOK},
		{Child: "logs", Column: "user_id", Parent: "users", Outcome: db.OutcomeTimedOut},
	}
	target := []relations.Shape{
		{Child: "PLAYERS", Column: "TEAM_ID", Parent: "TEAMS", Avg: 1, P95: 1, Max: 1, ZeroShare: 0, Outcome: db.OutcomeOK},
		{Child: "badges", Column: "player_id", Parent: "players", Avg: 2, P95: 2, Max: 2, Outcome: db.OutcomeOK},
		{Child: "extra", Column: "owner_id", Parent: "users", Avg: 4, Outcome: db.OutcomeOK},
		{Child: "logs", Column: "user_id", Parent: "users", Avg: 4, Outcome: db.OutcomeOK},
	}
	drift := DiffShapes(source, target)
	byKey := map[string]ShapeDrift{}
	for _, d := range drift {
		byKey[strings.ToLower(d.Child+"."+d.Column)] = d
	}
	players := byKey["players.team_id"]
	if players.Status != ShapeDiffers || math.Abs(players.AvgDelta-(1-3.75)) > 1e-9 || players.MaxDelta != -9 {
		t.Fatalf("players.team_id = %+v", players)
	}
	if byKey["badges.player_id"].Status != ShapeSame {
		t.Fatalf("badges = %+v", byKey["badges.player_id"])
	}
	if len(drift) != 4 {
		t.Fatalf("drift rows = %d, want 4", len(drift))
	}
	if byKey["logs.user_id"].Status != ShapeUnknown || byKey["extra.owner_id"].Status != ShapeTargetOnly {
		t.Fatalf("unmatched/unknown = %+v / %+v", byKey["logs.user_id"], byKey["extra.owner_id"])
	}
}

func TestRenderShapeDrift_MarksEstimatesUnknownAndHidesMatches(t *testing.T) {
	drift := DiffShapes(
		[]relations.Shape{
			{Child: "orders", Column: "user_id", Parent: "users", Avg: 2.5, P95: 6, Max: 9, ZeroShare: 0.25, Outcome: db.OutcomeOK},
			{Child: "same", Column: "p_id", Parent: "p", Avg: 1, P95: 1, Max: 1, Outcome: db.OutcomeOK},
			{Child: "events", Column: "user_id", Parent: "users", Avg: 3, P95: -1, Max: -1, Outcome: relations.OutcomeEstimated},
		},
		[]relations.Shape{
			{Child: "orders", Column: "user_id", Parent: "users", Avg: 1, P95: 1, Max: 1, Outcome: db.OutcomeOK},
			{Child: "same", Column: "p_id", Parent: "p", Avg: 1, P95: 1, Max: 1, Outcome: db.OutcomeOK},
			{Child: "events", Column: "user_id", Parent: "users", Outcome: db.OutcomeTimedOut},
		},
	)
	var b strings.Builder
	RenderShapeDrift(&b, drift, true)
	out := b.String()
	for _, want := range []string{"orders.user_id → users", "2.50 / 6 / 9 · 25%", "1.00 / 1 / 1 · 0%", "~3.00 / ? / ?", string(db.OutcomeTimedOut), "same 1 · differs 1", "unknown 1"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "same.p_id") {
		t.Errorf("only-diff output shows a matching relationship:\n%s", out)
	}
}
