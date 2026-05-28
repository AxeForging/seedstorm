package faker

import (
	"fmt"
	"testing"

	"github.com/AxeForging/seedstorm/internal/schema"
	"github.com/brianvoe/gofakeit/v6"
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

func TestValidFaker_valuesWithParentheses(t *testing.T) {
	// AI-generated randomstring values can contain parentheses, e.g. "Coffee Beans (500g)"
	f := "randomstring(Wireless Headphones,Coffee Beans (500g),Electric Kettle (1.7L))"
	if !ValidFaker(f) {
		t.Errorf("ValidFaker(%q) = false, want true — values with parens should work", f)
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

func TestGenerate_skipsGeneratedColumns(t *testing.T) {
	s := &schema.Schema{Tables: map[string]schema.Table{
		"orders": {Columns: map[string]schema.Column{
			"id":    {Type: "integer", PK: true},
			"total": {Type: "integer", Generated: true, Faker: "number(1,10)"},
		}},
	}}
	rows, err := Generate(s, []string{"orders"}, 1, 0, nil, "pgx")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, ok := rows["orders"][0]["total"]; ok {
		t.Fatalf("generated column should not be present in insert rows: %#v", rows["orders"][0])
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

// ── generate() faker type coverage ───────────────────────────────────────────

func TestGenerate_allFakerTypes(t *testing.T) {
	// Every supported faker type must produce a non-nil value of the right kind
	tests := []struct {
		faker   string
		wantStr bool // true = string result expected, false = any non-nil
	}{
		{"name", true},
		{"firstname", true},
		{"lastname", true},
		{"username", true},
		{"email", true},
		{"phone", true},
		{"street", true},
		{"city", true},
		{"state", true},
		{"country", true},
		{"zip", true},
		{"url", true},
		{"uuid", true},
		{"ipv4", true},
		{"macaddress", true},
		{"hexcolor", true},
		{"productname", true},
		{"company", true},
		{"jobtitle", true},
		{"word", true},
		{"sentence", true},
		{"date", true},
		{"time", true},
		{"json", true},
	}
	for _, tt := range tests {
		t.Run(tt.faker, func(t *testing.T) {
			val, err := generate(tt.faker)
			if err != nil {
				t.Fatalf("generate(%q): %v", tt.faker, err)
			}
			if val == nil {
				t.Fatalf("generate(%q) returned nil", tt.faker)
			}
			if tt.wantStr {
				if _, ok := val.(string); !ok {
					t.Errorf("generate(%q) returned %T, want string", tt.faker, val)
				}
			}
		})
	}
}

func TestGenerate_numericFakers(t *testing.T) {
	val, err := generate("number(1,100)")
	if err != nil {
		t.Fatal(err)
	}
	n, ok := val.(int)
	if !ok {
		t.Fatalf("number() returned %T, want int", val)
	}
	if n < 1 || n > 100 {
		t.Errorf("number(1,100) = %d, want 1-100", n)
	}
}

func TestGenerate_priceFaker(t *testing.T) {
	val, err := generate("price(10,500)")
	if err != nil {
		t.Fatal(err)
	}
	f, ok := val.(float64)
	if !ok {
		t.Fatalf("price() returned %T, want float64", val)
	}
	if f < 10 || f > 500 {
		t.Errorf("price(10,500) = %f, want 10-500", f)
	}
}

func TestGenerate_randomstringFaker(t *testing.T) {
	val, err := generate("randomstring(apple,banana,cherry)")
	if err != nil {
		t.Fatal(err)
	}
	s, ok := val.(string)
	if !ok {
		t.Fatalf("randomstring() returned %T, want string", val)
	}
	if s != "apple" && s != "banana" && s != "cherry" {
		t.Errorf("randomstring() = %q, want one of apple/banana/cherry", s)
	}
}

func TestGenerate_randomstringWithParens(t *testing.T) {
	val, err := generate("randomstring(Coffee (500g),Tea (250g))")
	if err != nil {
		t.Fatal(err)
	}
	s, ok := val.(string)
	if !ok {
		t.Fatalf("returned %T, want string", val)
	}
	if s != "Coffee (500g)" && s != "Tea (250g)" {
		t.Errorf("got %q, want one of the values with parens", s)
	}
}

func TestGenerate_paragraphFaker(t *testing.T) {
	val, err := generate("paragraph(2)")
	if err != nil {
		t.Fatal(err)
	}
	s, ok := val.(string)
	if !ok {
		t.Fatalf("paragraph() returned %T, want string", val)
	}
	if len(s) < 20 {
		t.Errorf("paragraph(2) too short: %q", s)
	}
}

func TestGenerate_boolFaker(t *testing.T) {
	val, err := generate("bool")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := val.(bool); !ok {
		t.Errorf("bool returned %T, want bool", val)
	}
}

func TestGenerate_datetimeFaker(t *testing.T) {
	val, err := generate("datetime")
	if err != nil {
		t.Fatal(err)
	}
	if val == nil {
		t.Fatal("datetime returned nil")
	}
	// datetime returns time.Time
	if fmt.Sprintf("%T", val) != "time.Time" {
		t.Errorf("datetime returned %T, want time.Time", val)
	}
}

func TestGenerate_emptyFaker(t *testing.T) {
	val, err := generate("")
	if err != nil {
		t.Fatal(err)
	}
	if val != nil {
		t.Errorf("empty faker should return nil, got %v", val)
	}
}

func TestGenerate_unknownFakerFallsBackToWord(t *testing.T) {
	val, err := generate("notarealfunction")
	if err != nil {
		t.Fatal(err)
	}
	if val == nil {
		t.Fatal("unknown faker should return a fallback word, got nil")
	}
	if _, ok := val.(string); !ok {
		t.Errorf("unknown faker fallback should be string, got %T", val)
	}
}

func TestGenerate_float64Faker(t *testing.T) {
	val, err := generate("float64")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := val.(float64); !ok {
		t.Errorf("float64 returned %T, want float64", val)
	}
}

func TestGenerate_latitudeLongitude(t *testing.T) {
	lat, err := generate("latitude")
	if err != nil {
		t.Fatal(err)
	}
	lng, err := generate("longitude")
	if err != nil {
		t.Fatal(err)
	}
	latF, ok := lat.(float64)
	if !ok {
		t.Fatalf("latitude returned %T", lat)
	}
	lngF, ok := lng.(float64)
	if !ok {
		t.Fatalf("longitude returned %T", lng)
	}
	if latF < -90 || latF > 90 {
		t.Errorf("latitude %f out of range", latF)
	}
	if lngF < -180 || lngF > 180 {
		t.Errorf("longitude %f out of range", lngF)
	}
}

// ── generateValue edge cases ────────────────────────────────────────────────

func TestGenerateValue_FKReturnsExistingPK(t *testing.T) {
	col := schema.Column{Type: "integer", FK: "users.id"}
	pks := map[string][]interface{}{"users": {10, 20, 30}}
	val, err := generateValue(col, "user_id", "orders", pks, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	v := val.(int)
	if v != 10 && v != 20 && v != 30 {
		t.Errorf("FK value %d not in parent PKs [10,20,30]", v)
	}
}

func TestGenerateValue_NullableFKWithNoParents(t *testing.T) {
	col := schema.Column{Type: "integer", FK: "depts.id", Nullable: true}
	pks := map[string][]interface{}{} // no parent PKs
	val, err := generateValue(col, "dept_id", "employees", pks, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if val != nil {
		t.Errorf("nullable FK with no parents should be nil, got %v", val)
	}
}

func TestGenerateValue_NullableSelfRefFKReturnsNil(t *testing.T) {
	col := schema.Column{Type: "integer", FK: "cats.id", Nullable: true}
	pks := map[string][]interface{}{} // no PKs yet for self
	val, err := generateValue(col, "parent_id", "cats", pks, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if val != nil {
		t.Errorf("nullable self-ref FK with no PKs should be nil, got %v", val)
	}
}

func TestGenerateValue_NonNullableFKWithNoParents_Errors(t *testing.T) {
	col := schema.Column{Type: "integer", FK: "missing.id"}
	pks := map[string][]interface{}{}
	_, err := generateValue(col, "missing_id", "orders", pks, nil, "")
	if err == nil {
		t.Fatal("expected error for non-nullable FK with no parent PKs")
	}
}

func TestGenerateValue_NumericCoercedToStringForVarchar(t *testing.T) {
	col := schema.Column{Type: "varchar", Faker: "number(1,100)"}
	pks := map[string][]interface{}{}
	val, err := generateValue(col, "code", "items", pks, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := val.(string); !ok {
		t.Errorf("numeric value for varchar column should be coerced to string, got %T", val)
	}
}

// ── reproducible seed ────────────────────────────────────────────────────────

func TestGenerate_reproducibleWithSeed(t *testing.T) {
	s := &schema.Schema{
		Tables: map[string]schema.Table{
			"products": {
				Columns: map[string]schema.Column{
					"id":    {Type: "integer", PK: true},
					"name":  {Type: "varchar", Faker: "productname"},
					"price": {Type: "numeric", Faker: "price(1,1000)"},
					"email": {Type: "varchar", Faker: "email"},
				},
			},
		},
	}

	// Generate twice with same seed
	gofakeit.Seed(12345)
	data1, err := Generate(s, []string{"products"}, 10, 0, nil, "pgx")
	if err != nil {
		t.Fatalf("Generate run 1: %v", err)
	}

	gofakeit.Seed(12345)
	data2, err := Generate(s, []string{"products"}, 10, 0, nil, "pgx")
	if err != nil {
		t.Fatalf("Generate run 2: %v", err)
	}

	if len(data1["products"]) != len(data2["products"]) {
		t.Fatalf("row count mismatch: %d vs %d", len(data1["products"]), len(data2["products"]))
	}

	// Compare every cell — with deterministic column iteration order via sorted keys
	for i := range data1["products"] {
		row1 := data1["products"][i]
		row2 := data2["products"][i]
		for col, v1 := range row1 {
			v2 := row2[col]
			if fmt.Sprintf("%v", v1) != fmt.Sprintf("%v", v2) {
				t.Errorf("row %d col %q: %v != %v", i, col, v1, v2)
			}
		}
	}
}

func TestGenerateFilteredWithCountsUsesGlobalRowsByDefault(t *testing.T) {
	s := &schema.Schema{
		Tables: map[string]schema.Table{
			"users": {
				Columns: map[string]schema.Column{
					"id":   {Type: "integer", PK: true},
					"name": {Type: "varchar", Faker: "name"},
				},
			},
			"orders": {
				Columns: map[string]schema.Column{
					"id":      {Type: "integer", PK: true},
					"user_id": {Type: "integer", FK: "users.id"},
				},
			},
		},
	}

	data, err := GenerateFilteredWithCounts(s, []string{"users", "orders"}, []string{"users", "orders"}, 3, 0, nil, nil, "pgx")
	if err != nil {
		t.Fatalf("GenerateFilteredWithCounts: %v", err)
	}
	if got := len(data["users"]); got != 3 {
		t.Fatalf("users rows = %d, want 3", got)
	}
	if got := len(data["orders"]); got != 3 {
		t.Fatalf("orders rows = %d, want 3", got)
	}
}

func TestGenerateFilteredWithCountsOverridesIndividualTables(t *testing.T) {
	s := &schema.Schema{
		Tables: map[string]schema.Table{
			"users": {
				Columns: map[string]schema.Column{
					"id":   {Type: "integer", PK: true},
					"name": {Type: "varchar", Faker: "name"},
				},
			},
			"orders": {
				Columns: map[string]schema.Column{
					"id":      {Type: "integer", PK: true},
					"user_id": {Type: "integer", FK: "users.id"},
				},
			},
		},
	}

	data, err := GenerateFilteredWithCounts(s, []string{"users", "orders"}, []string{"users", "orders"}, 2, 0, map[string]int{
		"orders": 5,
	}, nil, "pgx")
	if err != nil {
		t.Fatalf("GenerateFilteredWithCounts: %v", err)
	}
	if got := len(data["users"]); got != 2 {
		t.Fatalf("users rows = %d, want 2", got)
	}
	if got := len(data["orders"]); got != 5 {
		t.Fatalf("orders rows = %d, want 5", got)
	}
}

func TestGenerateFilteredWithCountsUsesOverrideAsExactEnumTableVolume(t *testing.T) {
	s := &schema.Schema{
		Tables: map[string]schema.Table{
			"tickets": makeEnumTable(map[string][]string{
				"status": {"open", "closed"},
			}),
		},
	}

	data, err := GenerateFilteredWithCounts(s, []string{"tickets"}, []string{"tickets"}, 1, 0, map[string]int{
		"tickets": 3,
	}, nil, "pgx")
	if err != nil {
		t.Fatalf("GenerateFilteredWithCounts: %v", err)
	}
	if got := len(data["tickets"]); got != 3 {
		t.Fatalf("tickets rows = %d, want exact table override 3", got)
	}
}

func TestGenerateFilteredWithCountsOverrideWinsOverEnumRows(t *testing.T) {
	s := &schema.Schema{
		Tables: map[string]schema.Table{
			"tickets": makeEnumTable(map[string][]string{
				"status": {"open", "closed"},
			}),
		},
	}

	data, err := GenerateFilteredWithCounts(s, []string{"tickets"}, []string{"tickets"}, 1, 2, map[string]int{
		"tickets": 7,
	}, nil, "pgx")
	if err != nil {
		t.Fatalf("GenerateFilteredWithCounts: %v", err)
	}
	if got := len(data["tickets"]); got != 7 {
		t.Fatalf("tickets rows = %d, want table override 7 instead of enumRows total 4", got)
	}
}

func TestGenerateNullableSelfReferenceCreatesRootRow(t *testing.T) {
	s := &schema.Schema{
		Tables: map[string]schema.Table{
			"categories": {
				Columns: map[string]schema.Column{
					"id":        {Type: "integer", PK: true},
					"parent_id": {Type: "integer", FK: "categories.id", Nullable: true},
				},
			},
		},
	}

	data, err := Generate(s, []string{"categories"}, 3, 0, nil, "pgx")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got := len(data["categories"]); got != 3 {
		t.Fatalf("categories rows = %d, want 3", got)
	}
	if data["categories"][0]["parent_id"] != nil {
		t.Fatalf("first self-referential row should be a NULL root, got %v", data["categories"][0]["parent_id"])
	}
	if got := maxSelfRefDepth(data["categories"], "id", "parent_id"); got > DefaultSelfRefDepth {
		t.Fatalf("self-reference depth = %d, want <= %d", got, DefaultSelfRefDepth)
	}
}

func TestGenerateHardSelfReferenceBackfillsValidManagers(t *testing.T) {
	s := &schema.Schema{
		Tables: map[string]schema.Table{
			"employees": {
				Columns: map[string]schema.Column{
					"id":         {Type: "integer", PK: true},
					"manager_id": {Type: "integer", FK: "employees.id"},
				},
			},
		},
	}

	data, err := GenerateWithOptions(s, []string{"employees"}, 5, 0, nil, "pgx", GenerateOptions{SelfRefDepth: 2})
	if err != nil {
		t.Fatalf("GenerateWithOptions: %v", err)
	}
	rows := data["employees"]
	if got := len(rows); got != 5 {
		t.Fatalf("employees rows = %d, want 5", got)
	}
	if rows[0]["manager_id"] == nil {
		t.Fatal("non-nullable self-reference should be backfilled on first row")
	}
	if rows[0]["manager_id"] != rows[0]["id"] {
		t.Fatalf("first hard self-reference should self-root, got manager_id=%v id=%v", rows[0]["manager_id"], rows[0]["id"])
	}
	ids := map[interface{}]bool{}
	for _, row := range rows {
		ids[row["id"]] = true
	}
	for i, row := range rows {
		if row["manager_id"] == nil {
			t.Fatalf("row %d manager_id is nil for non-nullable self-FK", i)
		}
		if !ids[row["manager_id"]] {
			t.Fatalf("row %d manager_id=%v does not reference generated employee IDs %v", i, row["manager_id"], ids)
		}
	}
	if got := maxSelfRefDepth(rows, "id", "manager_id"); got > 2 {
		t.Fatalf("self-reference depth = %d, want <= 2", got)
	}
}

func TestGenerateWithOptionsSelfRefDepthZeroDoesNotBuildNullableChain(t *testing.T) {
	s := &schema.Schema{
		Tables: map[string]schema.Table{
			"categories": {
				Columns: map[string]schema.Column{
					"id":        {Type: "integer", PK: true},
					"parent_id": {Type: "integer", FK: "categories.id", Nullable: true},
				},
			},
		},
	}

	data, err := GenerateWithOptions(s, []string{"categories"}, 4, 0, nil, "pgx", GenerateOptions{SelfRefDepth: 0})
	if err != nil {
		t.Fatalf("GenerateWithOptions: %v", err)
	}
	for i, row := range data["categories"] {
		if row["parent_id"] != nil {
			t.Fatalf("row %d parent_id = %v, want nil when self-ref depth is 0", i, row["parent_id"])
		}
	}
}

func maxSelfRefDepth(rows []map[string]interface{}, pkCol, fkCol string) int {
	byID := make(map[interface{}]map[string]interface{}, len(rows))
	for _, row := range rows {
		byID[row[pkCol]] = row
	}

	maxDepth := 0
	for _, row := range rows {
		seen := map[interface{}]bool{}
		depth := 0
		current := row
		for {
			fk := current[fkCol]
			if fk == nil || seen[fk] {
				break
			}
			seen[fk] = true
			next := byID[fk]
			if next == nil || next[pkCol] == current[pkCol] {
				break
			}
			depth++
			current = next
		}
		if depth > maxDepth {
			maxDepth = depth
		}
	}
	return maxDepth
}

func TestGenerate_differentSeedsDifferentOutput(t *testing.T) {
	s := &schema.Schema{
		Tables: map[string]schema.Table{
			"items": {
				Columns: map[string]schema.Column{
					"id":   {Type: "integer", PK: true},
					"name": {Type: "varchar", Faker: "name"},
				},
			},
		},
	}

	gofakeit.Seed(111)
	data1, _ := Generate(s, []string{"items"}, 5, 0, nil, "pgx")

	gofakeit.Seed(222)
	data2, _ := Generate(s, []string{"items"}, 5, 0, nil, "pgx")

	// At least one row should differ
	different := false
	for i := range data1["items"] {
		if fmt.Sprintf("%v", data1["items"][i]["name"]) != fmt.Sprintf("%v", data2["items"][i]["name"]) {
			different = true
			break
		}
	}
	if !different {
		t.Error("expected different seeds to produce different data, but output was identical")
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
