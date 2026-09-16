package compare

import (
	"bytes"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AxeForging/seedstorm/internal/db"
)

func sampleSnapshot() Snapshot {
	return Snapshot{
		Label:     "app@db.example:5432",
		DBType:    "pgx",
		CountMode: CountEstimate,
		TakenAt:   time.Date(2026, 9, 16, 10, 30, 0, 123, time.UTC),
		Tables: map[string]TableStat{
			"users":       {Rows: 1200, Bytes: 65536, Columns: []string{"id", "email"}},
			"events":      {Rows: math.MaxInt32 * 4, Estimated: true, Bytes: db.UnknownCount, Columns: []string{"id"}},
			"USER_ENTITY": {Rows: db.UnknownCount, Bytes: db.UnknownCount},
			"empty":       {Rows: 0, Bytes: 8192},
		},
	}
}

func TestEncodeSnapshot_RoundTripsWithStableBytes(t *testing.T) {
	for _, format := range []string{FormatJSON, FormatYAML} {
		t.Run(format, func(t *testing.T) {
			want := sampleSnapshot()
			first, err := EncodeSnapshot(want, format)
			if err != nil {
				t.Fatalf("encode: %v", err)
			}
			got, err := ParseSnapshot(first)
			if err != nil {
				t.Fatalf("parse own output: %v\n%s", err, first)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("round trip changed the snapshot:\n got  %+v\n want %+v", got, want)
			}
			second, err := EncodeSnapshot(got, format)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(first, second) {
				t.Fatalf("re-encoding is not byte-stable:\n%s\n---\n%s", first, second)
			}
			if again, _ := EncodeSnapshot(want, format); !bytes.Equal(first, again) {
				t.Fatal("encoding the same snapshot twice differs (map order leaked)")
			}
		})
	}
}

func TestEncodeSnapshot_EnvelopeFirstAndTablesSorted(t *testing.T) {
	for _, format := range []string{FormatJSON, FormatYAML} {
		t.Run(format, func(t *testing.T) {
			out, err := EncodeSnapshot(sampleSnapshot(), format)
			if err != nil {
				t.Fatal(err)
			}
			text := string(out)
			order := []string{"kind", SnapshotKind, "version", "tables", "USER_ENTITY", "empty", "events", "users"}
			last := -1
			for _, token := range order {
				i := strings.Index(text, token)
				if i <= last {
					t.Fatalf("%q out of order (at %d, previous at %d):\n%s", token, i, last, text)
				}
				last = i
			}
		})
	}
}

func TestEncodeSnapshot_RejectsUnknownFormat(t *testing.T) {
	_, err := EncodeSnapshot(sampleSnapshot(), "csv")
	if err == nil || !strings.Contains(err.Error(), `unknown snapshot format "csv"`) {
		t.Fatalf("err = %v", err)
	}
}

func TestParseSnapshot_AcceptedInputs(t *testing.T) {
	cases := []struct {
		name  string
		input string
		check func(t *testing.T, s Snapshot)
	}{
		{
			name:  "minimal hand-written yaml",
			input: "tables:\n  users: 1200\n  orders: 5000\n",
			check: func(t *testing.T, s Snapshot) {
				want := map[string]TableStat{
					"users":  {Rows: 1200, Bytes: db.UnknownCount},
					"orders": {Rows: 5000, Bytes: db.UnknownCount},
				}
				if !reflect.DeepEqual(s.Tables, want) {
					t.Fatalf("tables = %+v", s.Tables)
				}
			},
		},
		{
			name:  "minimal flow yaml",
			input: "tables: {users: 1200, orders: 5000}",
			check: func(t *testing.T, s Snapshot) {
				if s.Tables["orders"].Rows != 5000 || len(s.Tables) != 2 {
					t.Fatalf("tables = %+v", s.Tables)
				}
			},
		},
		{
			name:  "minimal json",
			input: `{"tables": {"users": 1200}}`,
			check: func(t *testing.T, s Snapshot) {
				if s.Tables["users"].Rows != 1200 {
					t.Fatalf("tables = %+v", s.Tables)
				}
			},
		},
		{
			name:  "count beyond int32",
			input: "tables:\n  events: 9000000000000\n",
			check: func(t *testing.T, s Snapshot) {
				if s.Tables["events"].Rows != 9_000_000_000_000 {
					t.Fatalf("rows = %d", s.Tables["events"].Rows)
				}
			},
		},
		{
			name:  "max int64",
			input: "tables:\n  events: 9223372036854775807\n",
			check: func(t *testing.T, s Snapshot) {
				if s.Tables["events"].Rows != math.MaxInt64 {
					t.Fatalf("rows = %d", s.Tables["events"].Rows)
				}
			},
		},
		{
			name:  "unknown marker in minimal form",
			input: "tables:\n  stale: -1\n",
			check: func(t *testing.T, s Snapshot) {
				if s.Tables["stale"].Rows != db.UnknownCount {
					t.Fatalf("rows = %d", s.Tables["stale"].Rows)
				}
			},
		},
		{
			name: "full form mixes counts and objects, engine alias, unquoted timestamp",
			input: `kind: seedstorm.table-counts
version: 1
dbType: postgres
countMode: exact
takenAt: 2026-09-16T10:00:00Z
tables:
  users: 10
  orders: {rows: 20, estimated: true}
`,
			check: func(t *testing.T, s Snapshot) {
				if s.DBType != "pgx" || s.CountMode != CountExact || !s.TakenAt.Equal(time.Date(2026, 9, 16, 10, 0, 0, 0, time.UTC)) {
					t.Fatalf("header = %+v", s)
				}
				if got := s.Tables["orders"]; got.Rows != 20 || !got.Estimated || got.Bytes != db.UnknownCount {
					t.Fatalf("orders = %+v (bytes left out must stay unknown)", got)
				}
				if got := s.Tables["users"]; got.Rows != 10 || got.Estimated {
					t.Fatalf("users = %+v", got)
				}
			},
		},
		{
			name:  "names differing only by case are distinct tables",
			input: "tables:\n  Users: 1\n  users: 2\n",
			check: func(t *testing.T, s Snapshot) {
				if s.Tables["Users"].Rows != 1 || s.Tables["users"].Rows != 2 {
					t.Fatalf("tables = %+v", s.Tables)
				}
			},
		},
		{
			name:  "zero rows",
			input: "tables:\n  empty: 0\n",
			check: func(t *testing.T, s Snapshot) {
				if s.Tables["empty"].Rows != 0 {
					t.Fatalf("rows = %d", s.Tables["empty"].Rows)
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, err := ParseSnapshot([]byte(c.input))
			if err != nil {
				t.Fatalf("ParseSnapshot: %v", err)
			}
			c.check(t, s)
		})
	}
}

func TestParseSnapshot_RejectsWithReadableErrors(t *testing.T) {
	// rejects parses input and expects an error containing want.
	rejects := func(name, input, want string) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			_, err := ParseSnapshot([]byte(input))
			if err == nil {
				t.Fatalf("expected an error containing %q", want)
			}
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error = %q\nwant it to contain %q", err, want)
			}
		})
	}
	rejects("empty", "", "snapshot is empty")
	rejects("bad timestamp", "takenAt: yesterday\ntables: {a: 1}", `takenAt "yesterday" is not an RFC 3339 timestamp`)
	rejects("whitespace", "  \n\t", "snapshot is empty")
	rejects("bad count mode", "countMode: fast\ntables: {a: 1}", `unknown count mode "fast"`)
	rejects("not yaml", "{not json", "not valid JSON or YAML")
	rejects("bad db type", "dbType: oracle\ntables: {a: 1}", `dbType "oracle" is not supported`)
	rejects("top-level list", "[1, 2]", "expected an object with kind, version and tables, got a list")
	rejects("bad columns", "kind: seedstorm.table-counts\nversion: 1\ntables:\n  users: {rows: 1, columns: id}\n", "columns must be a list of names")
	rejects("top-level scalar", "hello", `got "hello"`)
	rejects("bad estimated", "kind: seedstorm.table-counts\nversion: 1\ntables:\n  users: {rows: 1, estimated: maybe}\n", "estimated must be true or false")
	rejects("wrong kind", "kind: seedstorm.profile\nversion: 1\ntables: {a: 1}", `kind is "seedstorm.profile", want "seedstorm.table-counts"`)
	rejects("missing rows", "kind: seedstorm.table-counts\nversion: 1\ntables:\n  users: {bytes: 3}\n", `table "users": missing rows`)
	rejects("missing version", "kind: seedstorm.table-counts\ntables: {a: 1}", "snapshot has no version (supported: 1)")
	rejects("unknown table field", "kind: seedstorm.table-counts\nversion: 1\ntables:\n  users: {rows: 1, size: 3}\n", `table "users": unexpected field "size"`)
	rejects("unsupported version", "kind: seedstorm.table-counts\nversion: 2\ntables: {a: 1}", "unsupported snapshot version 2 (supported: 1)")
	rejects("random json", `{"hello": "world"}`, `unexpected field(s) "hello"`)
	rejects("version without kind", "version: 1\ntables: {a: 1}", "add kind: seedstorm.table-counts")
	rejects("compare report json", `{"source": {}, "target": {}, "rows": [], "totals": {}}`, `unexpected field(s) "rows", "source", "target", "totals"`)
	rejects("no tables", "kind: seedstorm.table-counts\nversion: 1\nlabel: x", "snapshot has no tables")
	rejects("schema yaml in minimal form", "tables:\n  users:\n    columns:\n      id: {type: integer}\n", `table "users": expected a row count like `+"`users: 1200`")
	rejects("empty tables", "tables: {}", "snapshot has no tables")
	rejects("seed profile", "name: eval\nrules:\n  - column: email\ntables:\n  users:\n    rows: 13\n", "looks like a seed profile")
	rejects("tables is a list", "tables: [users, orders]", "snapshot tables must map table names to row counts, got a list")
	rejects("exact duplicate table json", `{"tables": {"users": 1, "users": 2}}`, `snapshot lists the same name twice`)
	rejects("negative rows minimal", "tables:\n  users: -5\n", `table "users": row count -5 is negative (use -1 for unknown)`)
	rejects("exact duplicate table yaml", "tables:\n  users: 1\n  users: 2\n", `snapshot lists the same name twice: [3:3] mapping key "users" already defined`)
	rejects("negative rows full", "kind: seedstorm.table-counts\nversion: 1\ntables:\n  users: {rows: -2}\n", `table "users": row count -2 is negative`)
	rejects("beyond int64", "tables:\n  users: 99999999999999999999\n", `table "users": expected a whole-number row count`)
	rejects("negative bytes", "kind: seedstorm.table-counts\nversion: 1\ntables:\n  users: {rows: 1, bytes: -9}\n", `table "users": size -9 is negative`)
	rejects("quoted rows", "tables:\n  users: \"12\"\n", `table "users": expected a whole-number row count, got "12"`)
	rejects("fractional rows", "tables:\n  users: 1.5\n", `table "users": expected a whole-number row count, got the number 1.5`)
}

func TestParseSnapshot_DiffsLikeATakenSnapshot(t *testing.T) {
	src, err := ParseSnapshot([]byte("tables: {USERS: 100, orders: 40}"))
	if err != nil {
		t.Fatal(err)
	}
	tgt := snap("stage", map[string]TableStat{"users": stat(100), "orders": stat(10)})
	r := Diff(src, tgt)
	if got := rowFor(t, r, "USERS"); got.Status != StatusSame || got.TargetTable != "users" {
		t.Errorf("USERS = %+v", got)
	}
	if got := rowFor(t, r, "orders"); got.Status != StatusDiffers || got.Delta != -30 {
		t.Errorf("orders = %+v", got)
	}
	if r.Totals.SourceBytes != 0 {
		t.Errorf("unknown sizes leaked into totals: %+v", r.Totals)
	}
}
