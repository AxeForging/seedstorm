package faker

import (
	"testing"

	"github.com/AxeForging/seedstorm/internal/schema"
)

// ── ValidFaker ───────────────────────────────────────────────────────────────

func TestValidFaker_knownBare(t *testing.T) {
	for _, f := range []string{"email", "name", "uuid", "word", "datetime", "bool", "json"} {
		if !ValidFaker(f) {
			t.Errorf("ValidFaker(%q) = false, want true", f)
		}
	}
}

func TestValidFaker_knownParameterised(t *testing.T) {
	for _, f := range []string{"number(1,100)", "price(10,500)", "randomstring(a,b,c)", "paragraph(2)"} {
		if !ValidFaker(f) {
			t.Errorf("ValidFaker(%q) = false, want true", f)
		}
	}
}

func TestValidFaker_empty(t *testing.T) {
	if !ValidFaker("") {
		t.Error("ValidFaker('') = false, want true")
	}
}

func TestValidFaker_unknown(t *testing.T) {
	for _, f := range []string{"fulladdress", "fakefunction", "randomint(5)", "gibberish(x,y)"} {
		if ValidFaker(f) {
			t.Errorf("ValidFaker(%q) = true, want false", f)
		}
	}
}

// makeEnumTable builds a minimal schema.Table with one or more enum columns.
func makeEnumTable(enumCols map[string][]string) schema.Table {
	cols := map[string]schema.Column{
		"id": {Type: "integer", PK: true},
	}
	for colName, vals := range enumCols {
		faker := "randomstring(" + joinVals(vals) + ")"
		cols[colName] = schema.Column{Type: "varchar", Faker: faker}
	}
	return schema.Table{Columns: cols}
}

func joinVals(vals []string) string {
	s := ""
	for i, v := range vals {
		if i > 0 {
			s += ","
		}
		s += v
	}
	return s
}

// ── findAllEnumColumns ────────────────────────────────────────────────────────

func TestFindAllEnumColumns_single(t *testing.T) {
	tbl := makeEnumTable(map[string][]string{
		"status": {"pending", "active", "closed"},
	})
	got := findAllEnumColumns(tbl)
	if len(got) != 1 {
		t.Fatalf("expected 1 enum column, got %d", len(got))
	}
	if _, ok := got["status"]; !ok {
		t.Error("expected 'status' in result")
	}
	if len(got["status"]) != 3 {
		t.Errorf("expected 3 values, got %d", len(got["status"]))
	}
}

func TestFindAllEnumColumns_multiple(t *testing.T) {
	tbl := makeEnumTable(map[string][]string{
		"status":   {"open", "closed"},
		"priority": {"low", "medium", "high"},
	})
	got := findAllEnumColumns(tbl)
	if len(got) != 2 {
		t.Fatalf("expected 2 enum columns, got %d", len(got))
	}
}

func TestFindAllEnumColumns_none(t *testing.T) {
	tbl := schema.Table{Columns: map[string]schema.Column{
		"id":   {Type: "integer", PK: true},
		"name": {Type: "varchar", Faker: "name"},
	}}
	got := findAllEnumColumns(tbl)
	if len(got) != 0 {
		t.Errorf("expected 0 enum columns, got %d", len(got))
	}
}

// ── topUpEnumCoverage ─────────────────────────────────────────────────────────

func TestTopUpEnumCoverage_topsUpToMinRows(t *testing.T) {
	tbl := makeEnumTable(map[string][]string{
		"status": {"pending", "active", "closed"},
	})
	// 2 rows for "pending", 0 for "active" and "closed"; minRows = 3
	data := map[string][]map[string]interface{}{
		"items": {
			{"id": 1, "status": "pending"},
			{"id": 2, "status": "pending"},
		},
	}
	pks := map[string][]interface{}{"items": {1, 2}}
	enumCols := findAllEnumColumns(tbl)

	if err := topUpEnumCoverage(data, pks, tbl, "items", enumCols, 3); err != nil {
		t.Fatalf("topUpEnumCoverage: %v", err)
	}

	counts := map[string]int{}
	for _, row := range data["items"] {
		if v, ok := row["status"].(string); ok {
			counts[v]++
		}
	}
	for _, want := range []string{"pending", "active", "closed"} {
		if counts[want] < 3 {
			t.Errorf("status=%q: expected >= 3 rows, got %d", want, counts[want])
		}
	}
	// pending had 2 → +1; active had 0 → +3; closed had 0 → +3; total = 2+1+3+3 = 9
	if len(data["items"]) != 9 {
		t.Errorf("expected 9 rows, got %d", len(data["items"]))
	}
}

func TestTopUpEnumCoverage_noOpWhenAllMeetMinRows(t *testing.T) {
	tbl := makeEnumTable(map[string][]string{
		"status": {"a", "b", "c"},
	})
	data := map[string][]map[string]interface{}{
		"t": {
			{"id": 1, "status": "a"},
			{"id": 2, "status": "a"},
			{"id": 3, "status": "b"},
			{"id": 4, "status": "b"},
			{"id": 5, "status": "c"},
			{"id": 6, "status": "c"},
		},
	}
	pks := map[string][]interface{}{"t": {1, 2, 3, 4, 5, 6}}
	enumCols := findAllEnumColumns(tbl)

	if err := topUpEnumCoverage(data, pks, tbl, "t", enumCols, 2); err != nil {
		t.Fatalf("topUpEnumCoverage: %v", err)
	}
	if len(data["t"]) != 6 {
		t.Errorf("expected 6 rows (no top-up needed), got %d", len(data["t"]))
	}
}

func TestTopUpEnumCoverage_multipleEnumColumns(t *testing.T) {
	tbl := makeEnumTable(map[string][]string{
		"status":   {"open", "closed"},
		"priority": {"low", "high"},
	})
	// Both rows have only "open" status and "low" priority; minRows = 2
	data := map[string][]map[string]interface{}{
		"tickets": {
			{"id": 1, "status": "open", "priority": "low"},
			{"id": 2, "status": "open", "priority": "low"},
		},
	}
	pks := map[string][]interface{}{"tickets": {1, 2}}
	enumCols := findAllEnumColumns(tbl)

	if err := topUpEnumCoverage(data, pks, tbl, "tickets", enumCols, 2); err != nil {
		t.Fatalf("topUpEnumCoverage: %v", err)
	}

	counts := map[string]map[string]int{
		"status":   {},
		"priority": {},
	}
	for _, row := range data["tickets"] {
		if v, ok := row["status"].(string); ok {
			counts["status"][v]++
		}
		if v, ok := row["priority"].(string); ok {
			counts["priority"][v]++
		}
	}
	for _, want := range []string{"open", "closed"} {
		if counts["status"][want] < 2 {
			t.Errorf("status=%q: expected >= 2 rows, got %d", want, counts["status"][want])
		}
	}
	for _, want := range []string{"low", "high"} {
		if counts["priority"][want] < 2 {
			t.Errorf("priority=%q: expected >= 2 rows, got %d", want, counts["priority"][want])
		}
	}
}

// ── Generate with enum top-up ─────────────────────────────────────────────────

func TestGenerate_enumTopUp_singleColumn(t *testing.T) {
	s := &schema.Schema{
		Tables: map[string]schema.Table{
			"orders": makeEnumTable(map[string][]string{
				"status": {"pending", "processing", "shipped", "delivered", "cancelled"},
			}),
		},
	}
	const wantRows = 5
	data, err := Generate(s, []string{"orders"}, wantRows, 0, nil, "pgx")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	counts := map[string]int{}
	for _, row := range data["orders"] {
		if v, ok := row["status"].(string); ok {
			counts[v]++
		}
	}
	for _, want := range []string{"pending", "processing", "shipped", "delivered", "cancelled"} {
		if counts[want] < wantRows {
			t.Errorf("status=%q: expected >= %d rows, got %d", want, wantRows, counts[want])
		}
	}
}

func TestGenerate_enumTopUp_multipleColumns(t *testing.T) {
	s := &schema.Schema{
		Tables: map[string]schema.Table{
			"tickets": makeEnumTable(map[string][]string{
				"status":   {"open", "in_progress", "resolved", "closed"},
				"priority": {"low", "medium", "high", "critical"},
			}),
		},
	}
	const wantRows = 3
	data, err := Generate(s, []string{"tickets"}, wantRows, 0, nil, "pgx")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	statusCounts := map[string]int{}
	priorityCounts := map[string]int{}
	for _, row := range data["tickets"] {
		if v, ok := row["status"].(string); ok {
			statusCounts[v]++
		}
		if v, ok := row["priority"].(string); ok {
			priorityCounts[v]++
		}
	}
	for _, want := range []string{"open", "in_progress", "resolved", "closed"} {
		if statusCounts[want] < wantRows {
			t.Errorf("status=%q: expected >= %d rows, got %d", want, wantRows, statusCounts[want])
		}
	}
	for _, want := range []string{"low", "medium", "high", "critical"} {
		if priorityCounts[want] < wantRows {
			t.Errorf("priority=%q: expected >= %d rows, got %d", want, wantRows, priorityCounts[want])
		}
	}
}

func TestGenerate_noEnumColumns_rowCountUnchanged(t *testing.T) {
	s := &schema.Schema{
		Tables: map[string]schema.Table{
			"users": {
				Columns: map[string]schema.Column{
					"id":    {Type: "integer", PK: true},
					"email": {Type: "varchar", Faker: "email"},
				},
			},
		},
	}
	data, err := Generate(s, []string{"users"}, 10, 0, nil, "pgx")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(data["users"]) != 10 {
		t.Errorf("expected exactly 10 rows, got %d", len(data["users"]))
	}
}

// ── generatePK ───────────────────────────────────────────────────────────────

func TestGeneratePK_integerType(t *testing.T) {
	for _, colType := range []string{"integer", "int", "bigint", "smallint", "serial", "bigserial"} {
		v, err := generatePK(colType, 5)
		if err != nil {
			t.Fatalf("generatePK(%q): %v", colType, err)
		}
		if v != 6 {
			t.Errorf("generatePK(%q, 5) = %v, want 6", colType, v)
		}
	}
}

func TestGeneratePK_uuidType(t *testing.T) {
	v, err := generatePK("uuid", 0)
	if err != nil {
		t.Fatalf("generatePK(uuid): %v", err)
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("expected string, got %T", v)
	}
	// UUID format: 8-4-4-4-12 hex chars
	if len(s) != 36 || s[8] != '-' {
		t.Errorf("expected UUID format, got %q", s)
	}
}

func TestGeneratePK_varcharType(t *testing.T) {
	v, err := generatePK("varchar", 0)
	if err != nil {
		t.Fatalf("generatePK(varchar): %v", err)
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("expected string, got %T", v)
	}
	if len(s) != 36 {
		t.Errorf("expected UUID-length string PK, got %q", s)
	}
}

func TestGeneratePK_textType(t *testing.T) {
	v, err := generatePK("text", 0)
	if err != nil {
		t.Fatalf("generatePK(text): %v", err)
	}
	if _, ok := v.(string); !ok {
		t.Fatalf("expected string, got %T", v)
	}
}

func TestGenerate_uuidPKTable(t *testing.T) {
	s := &schema.Schema{
		Tables: map[string]schema.Table{
			"items": {
				Columns: map[string]schema.Column{
					"id":   {Type: "uuid", PK: true},
					"name": {Type: "varchar", Faker: "word"},
				},
			},
		},
	}
	data, err := Generate(s, []string{"items"}, 5, 0, nil, "pgx")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	seen := make(map[string]bool)
	for _, row := range data["items"] {
		id, ok := row["id"].(string)
		if !ok {
			t.Fatalf("expected string PK, got %T: %v", row["id"], row["id"])
		}
		if len(id) != 36 {
			t.Errorf("expected UUID PK, got %q", id)
		}
		if seen[id] {
			t.Errorf("duplicate UUID PK: %s", id)
		}
		seen[id] = true
	}
}

func TestGenerate_uuidPK_FKReference(t *testing.T) {
	// Parent has UUID PK, child references it via FK — should get valid UUID references
	s := &schema.Schema{
		Tables: map[string]schema.Table{
			"parents": {
				Columns: map[string]schema.Column{
					"id":   {Type: "uuid", PK: true},
					"name": {Type: "varchar", Faker: "word"},
				},
			},
			"children": {
				Columns: map[string]schema.Column{
					"id":        {Type: "integer", PK: true},
					"parent_id": {Type: "uuid", FK: "parents.id"},
				},
			},
		},
	}
	data, err := Generate(s, []string{"parents", "children"}, 3, 0, nil, "pgx")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// Collect parent UUIDs
	parentIDs := make(map[string]bool)
	for _, row := range data["parents"] {
		parentIDs[row["id"].(string)] = true
	}
	// Every child's parent_id must reference an existing parent
	for _, row := range data["children"] {
		pid, ok := row["parent_id"].(string)
		if !ok {
			t.Fatalf("expected string FK, got %T", row["parent_id"])
		}
		if !parentIDs[pid] {
			t.Errorf("child references non-existent parent UUID %s", pid)
		}
	}
}

// ── composite PK collision handling ──────────────────────────────────────────

func TestGenerateStandardRows_exhaustedPKSpace_returnsError(t *testing.T) {
	// Junction table: both columns are PK+FK. With only 1 PK in each parent,
	// the only possible composite key is (1,1). Requesting 2 rows must fail
	// with an error rather than silently inserting a duplicate.
	tbl := schema.Table{
		Columns: map[string]schema.Column{
			"a_id": {Type: "integer", PK: true, FK: "a.id"},
			"b_id": {Type: "integer", PK: true, FK: "b.id"},
		},
	}
	data := map[string][]map[string]interface{}{"junc": nil}
	pks := map[string][]interface{}{"a": {1}, "b": {1}}

	// 2 rows requested but only 1 unique composite key possible → must error.
	if err := generateStandardRows(data, pks, tbl, "junc", 2); err == nil {
		t.Fatal("expected error when composite PK space is exhausted, got nil")
	}
}

func TestGenerateEnumRows_exhaustedPKSpace_returnsError(t *testing.T) {
	// Junction table with both columns as PK+FK. With 1 PK in each parent the
	// only composite key is (1,1). Requesting 2 rows per enum value must fail
	// rather than silently inserting a duplicate.
	tbl := schema.Table{
		Columns: map[string]schema.Column{
			"a_id":   {Type: "integer", PK: true, FK: "a.id"},
			"b_id":   {Type: "integer", PK: true, FK: "b.id"},
			"status": {Type: "varchar", Faker: "randomstring(active,closed)"},
		},
	}
	data := map[string][]map[string]interface{}{"junc": nil}
	pks := map[string][]interface{}{"a": {1}, "b": {1}}

	// 2 enum rows per value but only 1 unique combo → must error.
	err := generateEnumRows(data, pks, tbl, "junc", "status", []string{"active", "closed"}, 2)
	if err == nil {
		t.Fatal("expected error when composite PK space is exhausted in generateEnumRows, got nil")
	}
}

func TestTopUpEnumCoverage_noCollisionWithExistingRows(t *testing.T) {
	// Simulate a junction table that also carries an enum column.
	// Existing row occupies (1,1); top-up must not generate another (1,1).
	tbl := schema.Table{
		Columns: map[string]schema.Column{
			"a_id":   {Type: "integer", PK: true, FK: "a.id"},
			"b_id":   {Type: "integer", PK: true, FK: "b.id"},
			"status": {Type: "varchar", Faker: "randomstring(active,closed)"},
		},
	}
	existing := map[string]interface{}{"a_id": 1, "b_id": 1, "status": "active"}
	data := map[string][]map[string]interface{}{"junc": {existing}}
	// Two PKs in each parent → combinations: (1,1),(1,2),(2,1),(2,2)
	pks := map[string][]interface{}{"a": {1, 2}, "b": {1, 2}, "junc": {}}

	enumCols := findAllEnumColumns(tbl)
	if err := topUpEnumCoverage(data, pks, tbl, "junc", enumCols, 1); err != nil {
		t.Fatalf("topUpEnumCoverage: %v", err)
	}

	// Verify no duplicate composite PKs in the result.
	seen := make(map[string]int)
	for _, row := range data["junc"] {
		key := compositePKKey(row, tbl)
		seen[key]++
		if seen[key] > 1 {
			t.Errorf("duplicate composite PK found: %s", key)
		}
	}
}
