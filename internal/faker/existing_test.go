package faker

import (
	"testing"
	"time"

	"github.com/AxeForging/seedstorm/internal/schema"
)

func TestNextSequentialPK_ContinuesAfterLargestExistingID(t *testing.T) {
	cases := []struct {
		name string
		pool []interface{}
		want int
	}{
		{"empty pool starts at zero", nil, 0},
		{"dense ids use the pool size", []interface{}{int64(1), int64(2), int64(3)}, 3},
		{"sparse ids use the max", []interface{}{int64(1), int64(3), int64(9)}, 9},
		{"generated ints mixed with scanned int64", []interface{}{int64(4), 5}, 5},
		{"non-numeric pool falls back to size", []interface{}{"a", "b"}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := nextSequentialPK(c.pool); got != c.want {
				t.Fatalf("nextSequentialPK(%v) = %d, want %d", c.pool, got, c.want)
			}
		})
	}
}

func TestSortIntPool_OrdersIntegersAndLeavesMixedPoolsAlone(t *testing.T) {
	ints := []interface{}{int64(9), int64(2), 5}
	sortIntPool(ints)
	if ints[0] != int64(2) || ints[2] != int64(9) {
		t.Fatalf("int pool not sorted: %v", ints)
	}
	mixed := []interface{}{"b", int64(1), "a"}
	sortIntPool(mixed)
	if mixed[0] != "b" || mixed[2] != "a" {
		t.Fatalf("mixed pool reordered: %v", mixed)
	}
}

func TestNormalizeScanned_ParsesMySQLTextProtocolBytes(t *testing.T) {
	if got := normalizeScanned([]byte("42")); got != int64(42) {
		t.Fatalf("digits = %#v, want int64(42)", got)
	}
	if got := normalizeScanned([]byte("abc")); got != "abc" {
		t.Fatalf("text = %#v, want string", got)
	}
	if got := normalizeScanned(int64(7)); got != int64(7) {
		t.Fatalf("passthrough = %#v", got)
	}
}

func TestKeyValue_GeneratedAndScannedValuesMatch(t *testing.T) {
	day := time.Date(2001, 2, 3, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		colType   string
		generated interface{}
		scanned   interface{}
	}{
		{"integer", "integer", 7, int64(7)},
		{"mysql bytes", "int", 7, []byte("7")},
		{"date string vs time", "date", "2001-02-03", day},
		{"datetime vs string", "timestamp", day.Add(90 * time.Minute), "2001-02-03 01:30:00"},
		{"time", "time", "10:11:12", "10:11:12"},
		{"uuid", "uuid", "a-b", "a-b"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if a, b := keyValue(c.colType, c.generated), keyValue(c.colType, c.scanned); a != b {
				t.Fatalf("keyValue mismatch: generated %q vs scanned %q", a, b)
			}
		})
	}
}

func TestSequenceStartAfter_NextValueExceedsStoredMax(t *testing.T) {
	cases := []struct {
		name    string
		colType string
		max     interface{}
	}{
		{"bigint", "bigint", int64(41)},
		{"date", "date", time.Date(2000, 3, 1, 0, 0, 0, 0, time.UTC)},
		{"datetime", "timestamp", "2000-01-01 00:10:00"},
		{"time", "time", "00:02:00"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start := sequenceStartAfter(c.colType, c.max)
			next := uniqueSequenceValue(c.colType, start)
			if keyValue(c.colType, next) <= keyValue(c.colType, c.max) && c.colType != "bigint" {
				t.Fatalf("next %v does not exceed max %v", next, c.max)
			}
			if c.colType == "bigint" && next.(int) <= 41 {
				t.Fatalf("next %v does not exceed 41", next)
			}
		})
	}
	if got := sequenceStartAfter("bigint", nil); got != 0 {
		t.Fatalf("nil max start = %d, want 0", got)
	}
}

func junctionTable() schema.Table {
	return schema.Table{Columns: map[string]schema.Column{
		"a_id": {Type: "integer", PK: true, FK: "a.id"},
		"b_id": {Type: "integer", PK: true, FK: "b.id"},
	}}
}

func TestGenerateCompositeFKPKRows_SkipsCombinationsAlreadyStored(t *testing.T) {
	tbl := junctionTable()
	pks := map[string][]interface{}{"a": {int64(1), int64(2)}, "b": {int64(1), int64(2)}}
	existing := keysOf(
		compositePKKey(map[string]interface{}{"a_id": int64(1), "b_id": int64(1)}, tbl),
		compositePKKey(map[string]interface{}{"a_id": int64(2), "b_id": int64(1)}, tbl),
	)
	data := map[string][]map[string]interface{}{"junc": nil}

	generated, handled, err := generateCompositeFKPKRows(data, pks, tbl, "junc", 2, existing)
	if err != nil || !handled {
		t.Fatalf("generateCompositeFKPKRows: handled=%v err=%v", handled, err)
	}
	if generated != 2 {
		t.Fatalf("generated %d rows, want the 2 free combinations", generated)
	}
	for _, row := range data["junc"] {
		if existing.Has(compositePKKey(row, tbl)) {
			t.Fatalf("generated a stored combination: %v", row)
		}
	}
}

func TestGenerateCompositeFKPKRows_CapsWhenStoredRowsFillTheSpace(t *testing.T) {
	tbl := junctionTable()
	pks := map[string][]interface{}{"a": {int64(1)}, "b": {int64(1), int64(2)}}
	existing := keysOf(compositePKKey(map[string]interface{}{"a_id": 1, "b_id": 1}, tbl))
	data := map[string][]map[string]interface{}{"junc": nil}
	generated, _, err := generateCompositeFKPKRows(data, pks, tbl, "junc", 5, existing)
	if err != nil {
		t.Fatal(err)
	}
	if generated != 1 {
		t.Fatalf("generated %d, want 1 (only one free combination)", generated)
	}
}

func TestGenerateStandardRows_ContinuesAfterStoredIDs(t *testing.T) {
	tbl := schema.Table{Columns: map[string]schema.Column{
		"id":   {Type: "integer", PK: true},
		"name": {Type: "varchar", Faker: "word"},
	}}
	// Stored ids 1 and 7 (sparse): preloaded pool plus their keys.
	pks := map[string][]interface{}{"items": {int64(1), int64(7)}}
	existing := keysOf("id=1", "id=7")
	data := map[string][]map[string]interface{}{"items": nil}
	if err := generateStandardRows(data, pks, tbl, "items", 3, existing); err != nil {
		t.Fatal(err)
	}
	for i, want := range []int{8, 9, 10} {
		if got := data["items"][i]["id"]; got != want {
			t.Fatalf("row %d id = %v, want %d", i, got, want)
		}
	}
}

func TestGenerateStandardRows_FailsInsteadOfDuplicatingAStoredKey(t *testing.T) {
	tbl := schema.Table{Columns: map[string]schema.Column{
		"id": {Type: "integer", PK: true},
	}}
	// The key is stored but the pool was not preloaded, so every attempt yields
	// id=1: the guard must give up with an error, never emit the duplicate.
	data := map[string][]map[string]interface{}{"items": nil}
	err := generateStandardRows(data, map[string][]interface{}{}, tbl, "items", 1, keysOf("id=1"))
	if err == nil {
		t.Fatalf("expected an exhausted-key error, got rows %v", data["items"])
	}
}

func TestAssignUniqueSequences_ContinuesFromStart(t *testing.T) {
	tbl := schema.Table{Columns: map[string]schema.Column{
		"ext": {Type: "bigint", Faker: uniqueSequenceFaker},
	}}
	rows := []map[string]interface{}{{}, {}}
	assignUniqueSequences(rows, tbl, map[string]int{"ext": 41})
	if rows[0]["ext"] != 42 || rows[1]["ext"] != 43 {
		t.Fatalf("sequence = %v, %v; want 42, 43", rows[0]["ext"], rows[1]["ext"])
	}
	fresh := []map[string]interface{}{{}}
	assignUniqueSequences(fresh, tbl, nil)
	if fresh[0]["ext"] != 1 {
		t.Fatalf("fresh sequence = %v, want 1", fresh[0]["ext"])
	}
}

// Keycloak keys sessions by (…, offline_flag varchar(4)): a string primary key
// must fit its declared length instead of always being a 36-char UUID.
func TestGenerateValue_StringPrimaryKeyRespectsDeclaredLength(t *testing.T) {
	cases := []struct {
		ddl string
		max int
	}{
		{"character varying(4)", 4},
		{"varchar(1)", 1},
		{"character varying(36)", 36},
		{"text", 36},
	}
	for _, c := range cases {
		t.Run(c.ddl, func(t *testing.T) {
			col := schema.Column{Type: "character varying", DDLType: c.ddl, PK: true}
			seen := map[string]bool{}
			for i := 0; i < 20; i++ {
				v, err := generateValue(col, "flag", "sessions", map[string][]interface{}{}, nil, "")
				if err != nil {
					t.Fatal(err)
				}
				s, ok := v.(string)
				if !ok || s == "" || len(s) > c.max {
					t.Fatalf("PK value %#v does not fit %s", v, c.ddl)
				}
				seen[s] = true
			}
			if c.max >= 4 && len(seen) < 15 {
				t.Fatalf("only %d distinct values in 20 draws for %s", len(seen), c.ddl)
			}
		})
	}
}

func TestKeysCannotCollide(t *testing.T) {
	cases := []struct {
		name  string
		table schema.Table
		want  bool
	}{
		{"integer id", schema.Table{Columns: map[string]schema.Column{"id": {Type: "integer", PK: true}}}, true},
		{"bigserial id", schema.Table{Columns: map[string]schema.Column{"id": {Type: "bigserial", PK: true}}}, true},
		{"uuid id", schema.Table{Columns: map[string]schema.Column{"id": {Type: "uuid", PK: true}}}, true},
		{"short string id", schema.Table{Columns: map[string]schema.Column{"id": {Type: "character varying", PK: true}}}, false},
		{"date id", schema.Table{Columns: map[string]schema.Column{"day": {Type: "date", PK: true}}}, false},
		{"integer id that is also a foreign key", schema.Table{Columns: map[string]schema.Column{"id": {Type: "integer", PK: true, FK: "users.id"}}}, false},
		{"composite key", junctionTable(), false},
		{"no key", schema.Table{Columns: map[string]schema.Column{"name": {Type: "text"}}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := keysCannotCollide(c.table); got != c.want {
				t.Fatalf("keysCannotCollide = %v, want %v", got, c.want)
			}
		})
	}
}

func TestEnumerateCompositeFKPKRows_ResumesAfterTheLastCombination(t *testing.T) {
	tbl := junctionTable()
	pks := map[string][]interface{}{"a": {int64(1), int64(2), int64(3)}, "b": {int64(1), int64(2), int64(3)}}
	data := map[string][]map[string]interface{}{"junc": nil}
	first, next, _, err := enumerateCompositeFKPKRows(data, pks, tbl, "junc", 4, nil, 0)
	if err != nil || first != 4 {
		t.Fatalf("first chunk: generated %d, err %v", first, err)
	}
	// No key set: only the cursor keeps the second chunk off the first one.
	second, _, _, err := enumerateCompositeFKPKRows(data, pks, tbl, "junc", 5, nil, next)
	if err != nil || second != 5 {
		t.Fatalf("second chunk: generated %d, err %v", second, err)
	}
	seen := map[string]bool{}
	for _, row := range data["junc"] {
		key := compositePKKey(row, tbl)
		if seen[key] {
			t.Fatalf("combination %s generated twice", key)
		}
		seen[key] = true
	}
	if len(seen) != 9 {
		t.Fatalf("%d distinct combinations, want all 9", len(seen))
	}
}

// MySQL DATETIME rounds fractional seconds on insert, so two generated
// timestamps half a second apart can land on the same stored key. The key
// guard must see them as the same key (CI: Duplicate entry for event_log).
func TestKeyValue_DatetimeKeysCompareAsTheDatabaseStoresThem(t *testing.T) {
	a := time.Date(2021, 9, 4, 13, 28, 24, 700_000_000, time.UTC)
	b := time.Date(2021, 9, 4, 13, 28, 25, 200_000_000, time.UTC)
	if ka, kb := keyValue("datetime", a), keyValue("datetime", b); ka != kb {
		t.Fatalf("keys %q and %q differ, but MySQL stores both as 2021-09-04 13:28:25", ka, kb)
	}
	stored := time.Date(2021, 9, 4, 13, 28, 25, 0, time.UTC)
	if keyValue("timestamp", a) != keyValue("timestamp", stored) {
		t.Fatal("a generated key must match the rounded value read back from the database")
	}
	if keyValue("datetime", time.Date(2021, 9, 4, 13, 28, 24, 400_000_000, time.UTC)) == keyValue("datetime", b) {
		t.Fatal("values in different seconds after rounding must stay distinct")
	}
}
