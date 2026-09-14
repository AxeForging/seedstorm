package rules

import (
	"strings"
	"testing"

	"github.com/AxeForging/seedstorm/internal/faker"
	"github.com/AxeForging/seedstorm/internal/schema"
)

// shopSchema mirrors the shapes rules must cope with in real apps: text, numeric,
// enum-like, temporal, uuid, UNIQUE, keys and generated columns.
func shopSchema() *schema.Schema {
	return &schema.Schema{Tables: map[string]schema.Table{
		"users": {Columns: map[string]schema.Column{
			"id":           {Type: "integer", PK: true},
			"email":        {Type: "varchar", DDLType: "varchar(40)", Faker: "uuid", Unique: true},
			"backup_email": {Type: "varchar", Faker: "email", Nullable: true},
			"status":       {Type: "varchar", Faker: "randomstring(active,banned)"},
			"age":          {Type: "integer", Faker: "number(18,90)"},
			"created_at":   {Type: "timestamp", Faker: "datetime"},
			"full_name":    {Type: "text", Faker: "name", Generated: true},
		}},
		"orders": {Columns: map[string]schema.Column{
			"id":       {Type: "integer", PK: true},
			"user_id":  {Type: "integer", FK: "users.id"},
			"email":    {Type: "varchar", Faker: "email"},
			"external": {Type: "uuid", Faker: "uuid"},
			"note":     {Type: "text", Faker: "sentence", Nullable: true},
		}},
	}}
}

func parse(t *testing.T, doc string) *RuleSet {
	t.Helper()
	rs, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return rs
}

func planFor(t *testing.T, rs *RuleSet, table, column string) ColumnPlan {
	t.Helper()
	for _, p := range rs.Explain(shopSchema(), table) {
		if p.Column == column {
			return p
		}
	}
	t.Fatalf("no plan for %s.%s", table, column)
	return ColumnPlan{}
}

func TestParse_YAMLRoundTripKeepsEveryActionKind(t *testing.T) {
	rs := parse(t, `
name: loadtest
rules:
  - name: tag emails
    column: "*email*"
    template: "lt+{{seq}}.{{auto}}"
  - table: orders
    column: note
    setNull: true
tables:
  users:
    rows: 50
    columns:
      status: { value: active }
      age: { value: 42 }
      created_at: { faker: datetime }
  orders:
    columns:
      note: { oneOf: [a, b] }
`)
	if rs.Version != Version || rs.Name != "loadtest" || len(rs.Rules) != 2 {
		t.Fatalf("parsed = %+v", rs)
	}
	if rs.Rules[0].Kind() != "template" || rs.Rules[1].Kind() != "setNull" {
		t.Fatalf("rule kinds = %q, %q", rs.Rules[0].Kind(), rs.Rules[1].Kind())
	}
	out, err := rs.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	again := parse(t, string(out))
	if again.Tables["users"].Rows != 50 || again.Tables["orders"].Columns["note"].Kind() != "oneOf" {
		t.Fatalf("round trip lost data:\n%s", out)
	}
	if valueString(again.Tables["users"].Columns["age"].Value) != "42" {
		t.Fatalf("numeric value = %#v", again.Tables["users"].Columns["age"].Value)
	}
}

func TestParse_RejectsMalformedYAML(t *testing.T) {
	if _, err := Parse([]byte("rules: [\n")); err == nil {
		t.Fatal("expected parse error")
	}
	rs, err := Parse([]byte("  \n"))
	if err != nil || rs.Version != Version {
		t.Fatalf("empty doc = %+v, %v", rs, err)
	}
}

func TestValidate_StructuralErrors(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want string
	}{
		{"no action", `rules: [{column: email}]`, "set one of"},
		{"two actions", `rules: [{column: email, value: x, setNull: true}]`, "set only one of"},
		{"missing column pattern", `rules: [{value: x}]`, "column pattern is required"},
		{"bad glob", `rules: [{column: "[", value: x}]`, "invalid column pattern"},
		{"unknown generator", `rules: [{column: email, faker: emial}]`, "unknown generator"},
		{"unknown template token", `rules: [{column: email, template: "a{{nope}}"}]`, "unknown generator"},
		{"unclosed template", `rules: [{column: email, template: "a{{seq"}]`, "unclosed"},
		{"negative rows", `tables: {users: {rows: -1}}`, "zero or positive"},
		{"future version", `version: 9`, "newer than"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			issues := parse(t, c.doc).Validate(nil)
			if !HasErrors(issues) || !strings.Contains(joinIssues(issues), c.want) {
				t.Fatalf("issues = %v, want error containing %q", issues, c.want)
			}
		})
	}
}

func TestValidate_AgainstSchema(t *testing.T) {
	cases := []struct {
		name     string
		doc      string
		severity Severity
		want     string
	}{
		{"explicit rule on PK", `tables: {users: {columns: {id: {value: 1}}}}`, SeverityError, "primary key"},
		{"explicit rule on FK", `tables: {orders: {columns: {user_id: {value: 1}}}}`, SeverityError, "foreign key"},
		{"explicit rule on generated", `tables: {users: {columns: {full_name: {value: x}}}}`, SeverityError, "generated"},
		{"null on NOT NULL", `tables: {users: {columns: {age: {setNull: true}}}}`, SeverityError, "NOT NULL"},
		{"text into integer", `tables: {users: {columns: {age: {value: forty}}}}`, SeverityError, "not a number"},
		{"template text into timestamp", `tables: {users: {columns: {created_at: {template: "x{{seq}}"}}}}`, SeverityError, "adds text"},
		{"unknown table is a warning", `tables: {ghosts: {rows: 5}}`, SeverityWarning, "not in this database"},
		{"unknown column is a warning", `tables: {users: {columns: {nope: {value: x}}}}`, SeverityWarning, "not in table"},
		{"unique fixed value", `tables: {users: {columns: {email: {value: a@b.c}}}}`, SeverityWarning, "UNIQUE"},
		{"unique template without seq", `rules: [{column: email, table: users, template: "x{{word}}"}]`, SeverityWarning, "include {{seq}}"},
		{"pattern that matches nothing", `rules: [{column: nothing_here, value: x}]`, SeverityWarning, "applies to no column"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			issues := parse(t, c.doc).Validate(shopSchema())
			found := false
			for _, i := range issues {
				if i.Severity == c.severity && strings.Contains(i.Message, c.want) {
					found = true
				}
			}
			if !found {
				t.Fatalf("issues = %v, want %s containing %q", issues, c.severity, c.want)
			}
		})
	}
}

func TestValidate_CleanRuleSetHasNoIssues(t *testing.T) {
	rs := parse(t, `
rules:
  - column: "*email"
    template: "lt+{{run}}.{{seq}}@{{domain}}"
tables:
  users:
    columns:
      age: { value: 30 }
`)
	if issues := rs.Validate(shopSchema()); len(issues) != 0 {
		t.Fatalf("issues = %v", issues)
	}
}

func TestExplain_ExplicitColumnBeatsPatternAndPatternOrderWins(t *testing.T) {
	rs := parse(t, `
rules:
  - column: email
    template: "first+{{auto}}"
  - column: email
    template: "second+{{auto}}"
tables:
  orders:
    columns:
      email: { value: fixed@x.io }
`)
	users := planFor(t, rs, "users", "email")
	if users.Source.Kind != "pattern" || users.Source.Index != 0 || !strings.Contains(users.Effective, "first") {
		t.Fatalf("users.email plan = %+v", users)
	}
	orders := planFor(t, rs, "orders", "email")
	if orders.Source.Kind != "column" || !strings.Contains(orders.Effective, "fixed@x.io") {
		t.Fatalf("orders.email plan = %+v", orders)
	}
}

func TestExplain_PatternRulesAdaptToEachColumn(t *testing.T) {
	rs := parse(t, `
rules:
  - column: "*"
    template: "ss_{{auto}}"
`)
	// Text columns take the template.
	if p := planFor(t, rs, "orders", "note"); p.Source.Kind != "pattern" {
		t.Fatalf("note plan = %+v", p)
	}
	// Keys, generated and non-text columns are skipped with a reason, keeping defaults.
	skipReasons := map[string]string{
		"users.id":         "primary key",
		"orders.user_id":   "foreign key",
		"users.full_name":  "generated",
		"users.age":        "adds text",
		"users.created_at": "adds text",
		"orders.external":  "adds text",
	}
	for target, reason := range skipReasons {
		table, column, _ := strings.Cut(target, ".")
		p := planFor(t, rs, table, column)
		if p.Source.Kind != "default" || len(p.Skipped) != 1 || !strings.Contains(p.Skipped[0].Reason, reason) {
			t.Errorf("%s plan = %+v, want default with skip %q", target, p, reason)
		}
	}
}

func TestExplain_SkippedPatternFallsThroughToNextCompatibleRule(t *testing.T) {
	rs := parse(t, `
rules:
  - column: "*"
    setNull: true
  - column: age
    value: 21
`)
	// age is NOT NULL: rule 1 is skipped, rule 2 applies.
	p := planFor(t, rs, "users", "age")
	if p.Source.Index != 1 || len(p.Skipped) != 1 {
		t.Fatalf("age plan = %+v", p)
	}
	// note is nullable: rule 1 applies.
	if n := planFor(t, rs, "orders", "note"); n.Source.Index != 0 || n.Action == nil || !n.Action.SetNull {
		t.Fatalf("note plan = %+v", n)
	}
}

func TestExplain_GlobsAreCaseInsensitive(t *testing.T) {
	rs := parse(t, `rules: [{table: "USERS", column: "Backup_*", value: x}]`)
	if p := planFor(t, rs, "users", "backup_email"); p.Source.Kind != "pattern" {
		t.Fatalf("plan = %+v", p)
	}
}

func TestCompile_ProducesOverridesTheGeneratorApplies(t *testing.T) {
	rs := parse(t, `
rules:
  - column: "*email*"
    template: "lt+{{seq}}-{{run}}@example.test"
tables:
  users:
    columns:
      status: { value: active }
      backup_email: { setNull: true }
      age: { value: 33 }
`)
	sc := shopSchema()
	overrides, err := rs.Compile(sc, "abc123")
	if err != nil {
		t.Fatal(err)
	}
	opts := faker.DefaultGenerateOptions()
	opts.Overrides = overrides
	data, err := faker.GenerateWithOptions(sc, []string{"users", "orders"}, 3, 0, nil, "pgx", opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(data["users"]) != 3 {
		t.Fatalf("users rows = %d, want 3 (status rule removes enum top-up)", len(data["users"]))
	}
	for i, row := range data["users"] {
		if want := "lt+" + itoa(i+1) + "-abc123@example.test"; row["email"] != want {
			t.Errorf("users[%d].email = %v, want %s", i, row["email"], want)
		}
		if row["status"] != "active" || row["backup_email"] != nil || row["age"] != int64(33) {
			t.Errorf("users[%d] = %v", i, row)
		}
	}
	for _, row := range data["orders"] {
		if s, _ := row["email"].(string); !strings.HasPrefix(s, "lt+") {
			t.Errorf("orders email = %v", row["email"])
		}
	}
}

func TestCompile_RefusesRuleSetsWithErrors(t *testing.T) {
	rs := parse(t, `tables: {users: {columns: {id: {value: 1}}}}`)
	if _, err := rs.Compile(shopSchema(), "r"); err == nil || !strings.Contains(err.Error(), "primary key") {
		t.Fatalf("err = %v", err)
	}
}

func TestCompile_NilRuleSetIsANoOp(t *testing.T) {
	var rs *RuleSet
	overrides, err := rs.Compile(shopSchema(), "r")
	if err != nil || overrides != nil {
		t.Fatalf("overrides = %v, err = %v", overrides, err)
	}
}

func TestMergeTableRows_ExplicitCountsWin(t *testing.T) {
	rs := parse(t, `tables: {users: {rows: 10}, orders: {rows: 20}}`)
	got := MergeTableRowsFor(rs, shopSchema(), map[string]int{"orders": 5, "ghost": 0})
	if got["users"] != 10 || got["orders"] != 5 || len(got) != 2 {
		t.Fatalf("merged = %v", got)
	}
	if MergeTableRowsFor(nil, nil, nil) != nil {
		t.Fatal("nil merge should be nil")
	}
}

func joinIssues(issues []Issue) string {
	parts := make([]string, len(issues))
	for i, is := range issues {
		parts[i] = is.String()
	}
	return strings.Join(parts, "\n")
}

func itoa(n int) string {
	return strings.TrimSpace(formatValue(n))
}

func TestExamples_ShowWhatEachRuleWritesOnItsFirstColumn(t *testing.T) {
	rs := parse(t, `
rules:
  - column: "*email*"
    template: "lt+{{seq}}@x.io"
  - column: nothing_matches
    value: x
  - column: age
    template: "{{number(40,40)}}"
`)
	examples := rs.Examples(shopSchema(), 3, "")
	if len(examples) != 3 {
		t.Fatalf("examples = %+v", examples)
	}
	first := examples[0]
	if first.Table != "orders" || first.Column != "email" || len(first.Values) != 3 || first.Values[2] != "lt+3@x.io" {
		t.Fatalf("email example = %+v", first)
	}
	if examples[1].Column != "" || len(examples[1].Values) != 0 {
		t.Fatalf("unmatched rule should have no example: %+v", examples[1])
	}
	if examples[2].Values[0] != "40" {
		t.Fatalf("numeric example = %+v", examples[2])
	}
	if viewing := rs.Examples(shopSchema(), 1, "users"); viewing[0].Table != "users" {
		t.Fatalf("preferred table ignored: %+v", viewing[0])
	}
}

func TestExampleValues_ReportsWhyAnActionCannotFitAColumn(t *testing.T) {
	sc := shopSchema()
	vals, err := ExampleValues(Action{Template: "vip_{{auto}}"}, sc.Tables["users"].Columns["backup_email"], "users", "backup_email", 2)
	if err != nil || len(vals) != 2 || !strings.HasPrefix(vals[0], "vip_") || !strings.Contains(vals[0], "@") {
		t.Fatalf("vals = %v, err = %v", vals, err)
	}
	if _, err := ExampleValues(Action{Value: "forty"}, sc.Tables["users"].Columns["age"], "users", "age", 2); err == nil || !strings.Contains(err.Error(), "not a number") {
		t.Fatalf("err = %v", err)
	}
	if _, err := ExampleValues(Action{SetNull: true}, sc.Tables["users"].Columns["age"], "users", "age", 1); err == nil {
		t.Fatal("NULL on NOT NULL should be refused")
	}
	if _, err := ExampleValues(Action{Value: 1}, sc.Tables["users"].Columns["id"], "users", "id", 1); err == nil {
		t.Fatal("primary key should be refused")
	}
}

func TestValidate_ExplainsARuleShadowedByAnEarlierOne(t *testing.T) {
	rs := parse(t, `
rules:
  - column: "*email*"
    template: "a+{{seq}}@x.io"
  - column: email
    template: "b+{{seq}}@x.io"
`)
	var msg string
	for _, i := range rs.Validate(shopSchema()) {
		if i.Path == "rules[1]" {
			msg = i.Message
		}
	}
	if !strings.Contains(msg, "already taken by #1") {
		t.Fatalf("rules[1] message = %q", msg)
	}
}

func TestValidate_WarnsWhenARuleNarrowsAMultiColumnUniqueGroup(t *testing.T) {
	sc := shopSchema()
	users := sc.Tables["users"]
	users.Unique = [][]string{{"status", "age"}}
	sc.Tables["users"] = users
	rs := parse(t, `tables: {users: {columns: {status: {oneOf: [a, b]}}}}`)
	found := false
	for _, i := range rs.Validate(sc) {
		if i.Severity == SeverityWarning && strings.Contains(i.Message, "part of UNIQUE (status, age)") {
			found = true
		}
	}
	if !found {
		t.Fatalf("issues = %v", rs.Validate(sc))
	}
}

func TestValidate_WarnsThatSeqAloneRepeatsOnTheNextRun(t *testing.T) {
	sc := shopSchema()
	users := sc.Tables["users"]
	users.Unique = [][]string{{"status", "backup_email"}, {"status", "age"}}
	sc.Tables["users"] = users
	rs := parse(t, `
tables:
  users:
    columns:
      email: { template: "u{{seq}}@x.io" }
      backup_email: { template: "b{{seq}}@x.io" }
      status: { value: fixed }
`)
	var msgs []string
	for _, i := range rs.Validate(sc) {
		msgs = append(msgs, i.Path+": "+i.Message)
	}
	joined := strings.Join(msgs, "\n")
	for _, want := range []string{
		"tables.users.columns.email: column is UNIQUE; {{seq}} restarts every run, add {{run}}",
		"tables.users.columns.backup_email: column is part of UNIQUE (status, backup_email); {{seq}} restarts every run, add {{run}}",
		"part of UNIQUE (status, backup_email) and (status, age)",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in:\n%s", want, joined)
		}
	}
	clean := parse(t, `tables: {users: {columns: {email: {template: "u{{run}}.{{seq}}@x.io"}}}}`)
	for _, i := range clean.Validate(sc) {
		if strings.Contains(i.Message, "restarts every run") {
			t.Fatalf("{{run}} template still warned: %v", i)
		}
	}
}

// Keycloak on MySQL keeps upper-case identifiers (USER_ENTITY.USERNAME) while
// Postgres folds them: one profile must drive both.
func upperSchema() *schema.Schema {
	return &schema.Schema{Tables: map[string]schema.Table{
		"USER_ENTITY": {Columns: map[string]schema.Column{
			"ID":       {Type: "varchar", PK: true},
			"USERNAME": {Type: "varchar", Faker: "word"},
			"ENABLED":  {Type: "bit", Faker: "bool"},
		}},
	}}
}

func TestExplicitRules_MatchTablesAndColumnsIgnoringCase(t *testing.T) {
	rs := parse(t, `
tables:
  user_entity:
    rows: 30
    columns:
      username: { template: "kc_{{run}}_{{seq}}" }
`)
	sc := upperSchema()
	for _, issue := range rs.Validate(sc) {
		t.Errorf("unexpected issue: %v", issue)
	}
	plans := rs.Explain(sc, "USER_ENTITY")
	var username ColumnPlan
	for _, p := range plans {
		if p.Column == "USERNAME" {
			username = p
		}
	}
	if username.Source.Kind != "column" {
		t.Fatalf("USERNAME plan = %+v, want the explicit rule", username)
	}
	overrides, err := rs.Compile(sc, "r1")
	if err != nil || overrides["USER_ENTITY"]["USERNAME"] == nil {
		t.Fatalf("compile = %v, %v", overrides, err)
	}
	if rows := MergeTableRowsFor(rs, sc, nil); rows["USER_ENTITY"] != 30 || len(rows) != 1 {
		t.Fatalf("rows = %v, want keyed by the schema's own table name", rows)
	}
	if rows := MergeTableRowsFor(rs, sc, map[string]int{"USER_ENTITY": 5}); rows["USER_ENTITY"] != 5 {
		t.Fatalf("explicit count must win: %v", rows)
	}
}

func TestExplicitRules_ExactNameWinsOverCaseInsensitive(t *testing.T) {
	sc := upperSchema()
	sc.Tables["user_entity"] = schema.Table{Columns: map[string]schema.Column{
		"id":       {Type: "varchar", PK: true},
		"username": {Type: "varchar", Faker: "word"},
	}}
	rs := parse(t, `tables: {user_entity: {columns: {username: {value: lower}}}}`)
	for _, p := range rs.Explain(sc, "USER_ENTITY") {
		if p.Column == "USERNAME" && p.Source.Kind == "column" {
			t.Fatalf("USER_ENTITY picked up the rule meant for the exact user_entity table")
		}
	}
}
