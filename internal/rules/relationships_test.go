package rules

import (
	"strings"
	"testing"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/faker"
	"github.com/AxeForging/seedstorm/internal/relations"
	"github.com/AxeForging/seedstorm/internal/schema"
)

func relationshipSchema() *schema.Schema {
	return &schema.Schema{Tables: map[string]schema.Table{
		"USERS":  {Columns: map[string]schema.Column{"ID": {Type: "int", PK: true}}},
		"ORDERS": {Columns: map[string]schema.Column{"ID": {Type: "int", PK: true}, "USER_ID": {Type: "int", FK: "USERS.ID"}, "PARENT_ID": {Type: "int", FK: "ORDERS.ID", Nullable: true}}},
	}}
}

// A profile with relationships is written as version 2 (older binaries refuse
// it); one without stays version 1; both parse back.
func TestRelationships_VersionAndRoundTrip(t *testing.T) {
	plain := &RuleSet{Name: "p", Ignore: []string{"audit_*"}}
	data, err := plain.Marshal()
	if err != nil || !strings.Contains(string(data), "version: 1") || strings.Contains(string(data), "relationships") {
		t.Fatalf("plain profile:\n%s %v", data, err)
	}
	shaped := &RuleSet{Name: "s", Relationships: map[string]Relationship{
		"orders.user_id": {Min: 1, Avg: 2.5, Max: 9, ZeroShare: 0.1, Histogram: []faker.ShapeBucket{{Min: 1, Max: 1, Parents: 4}, {Min: 2, Max: 3, Parents: 3}}},
	}}
	data, err = shaped.Marshal()
	if err != nil || !strings.Contains(string(data), "version: 2") {
		t.Fatalf("shaped profile:\n%s %v", data, err)
	}
	back, err := Parse(data)
	if err != nil || back.Relationships["orders.user_id"].Max != 9 || len(back.Relationships["orders.user_id"].Histogram) != 2 {
		t.Fatalf("parsed back = %+v %v", back, err)
	}
	if issues := back.Validate(nil); HasErrors(issues) {
		t.Fatalf("round-tripped profile invalid: %v", issues)
	}
	future, _ := Parse([]byte("version: 3\n"))
	if !HasErrors(future.Validate(nil)) {
		t.Fatal("version 3 was accepted")
	}
}

func TestRelationships_ValidateStructureAndSchema(t *testing.T) {
	rs := &RuleSet{Relationships: map[string]Relationship{
		"orders":            {Min: 1, Avg: 1, Max: 1},
		"orders.bad_range":  {Min: 5, Avg: 3, Max: 4},
		"orders.bad_avg":    {Min: 1, Avg: 9, Max: 4},
		"orders.bad_share":  {Min: 1, Avg: 2, Max: 4, ZeroShare: 1},
		"orders.bad_bucket": {Min: 1, Avg: 2, Max: 4, Histogram: []faker.ShapeBucket{{Min: 3, Max: 2}}},
	}}
	msgs := map[string]string{}
	for _, i := range rs.Validate(nil) {
		msgs[i.Path] = i.Message
	}
	for path, want := range map[string]string{
		"relationships.orders":                         "table.column",
		"relationships.orders.bad_range":               "below min",
		"relationships.orders.bad_avg":                 "outside",
		"relationships.orders.bad_share":               "shares",
		"relationships.orders.bad_bucket.histogram[0]": "bucket",
	} {
		if !strings.Contains(msgs[path], want) {
			t.Errorf("%s: %q, want %q", path, msgs[path], want)
		}
	}

	sc := relationshipSchema()
	ok := &RuleSet{Relationships: map[string]Relationship{
		"orders.user_id":   {Min: 1, Avg: 2, Max: 4},
		"orders.parent_id": {Min: 1, Avg: 1, Max: 2},
		"invoices.user_id": {Min: 1, Avg: 1, Max: 2},
	}}
	warnings := map[string]string{}
	for _, i := range ok.Validate(sc) {
		if i.Severity == SeverityError {
			t.Fatalf("unexpected error %v", i)
		}
		warnings[i.Path] = i.Message
	}
	if !strings.Contains(warnings["relationships.orders.parent_id"], "self-references") || !strings.Contains(warnings["relationships.invoices.user_id"], "not in this database") {
		t.Fatalf("warnings = %v", warnings)
	}
	if _, has := warnings["relationships.orders.user_id"]; has {
		t.Fatalf("a shapeable key was warned: %v", warnings)
	}
	shapes := ok.Shapes(sc)
	if len(shapes) != 1 || shapes["ORDERS.USER_ID"].Max != 4 {
		t.Fatalf("compiled shapes (schema names, shapeable only) = %v", shapes)
	}
}

func TestRelationshipsFromShapes_KeepsMeasuredOnly(t *testing.T) {
	got, skipped := RelationshipsFromShapes([]relations.Shape{
		{Child: "orders", Column: "user_id", Min: 1, Avg: 2.345678, Max: 7, ZeroShare: 0.123456, Outcome: db.OutcomeOK, Histogram: []db.DegreeBucket{{Min: 1, Max: 1, Parents: 3, Children: 3}}},
		{Child: "orders", Column: "coupon_id", Avg: 3, Max: -1, Outcome: relations.OutcomeEstimated},
		{Child: "logs", Column: "user_id", Outcome: db.OutcomeTimedOut},
		{Child: "tree", Column: "parent_id", Max: 3, SelfRef: true, Outcome: db.OutcomeOK},
	})
	r := got["orders.user_id"]
	if len(got) != 1 || r.Avg != 2.35 || r.ZeroShare != 0.1235 || r.Max != 7 || len(r.Histogram) != 1 {
		t.Fatalf("relationships = %+v", got)
	}
	if strings.Join(skipped, ",") != "orders.coupon_id,logs.user_id,tree.parent_id" {
		t.Fatalf("skipped = %v", skipped)
	}
}
