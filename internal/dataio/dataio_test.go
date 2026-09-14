package dataio

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

// sample holds values that break naive streaming: quotes, a colon, multi-line
// text (a YAML block scalar), unicode, NULL, and an empty table.
func sample() (order []string, data map[string][]map[string]interface{}) {
	users := make([]map[string]interface{}, 0, 7)
	for i := 1; i <= 7; i++ {
		users = append(users, map[string]interface{}{
			"id":     i,
			"name":   fmt.Sprintf("O'Brien #%d: \"quoted\"", i),
			"bio":    fmt.Sprintf("line one\n- not an item %d\n\nünïcode", i),
			"active": i%2 == 0,
			"score":  float64(i) + 0.5,
			"note":   nil,
		})
	}
	data = map[string][]map[string]interface{}{
		"users":       users,
		"empty_table": {},
		"posts":       {{"id": 1, "user_id": 3, "title": "- dash first"}},
	}
	return []string{"empty_table", "posts", "users"}, data
}

func write(t *testing.T, format string, order []string, data map[string][]map[string]interface{}, piece int) string {
	t.Helper()
	var buf bytes.Buffer
	w, err := NewWriter(&buf, format, "pgx", 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range order {
		if err := w.Table(table); err != nil {
			t.Fatal(err)
		}
		rows := data[table]
		for i := 0; i < len(rows); i += piece {
			if err := w.Rows(rows[i:min(i+piece, len(rows))]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// read collects a document and checks no call exceeds chunk rows.
func read(t *testing.T, doc string, chunk int) ([]string, map[string][]map[string]interface{}) {
	t.Helper()
	var order []string
	got := map[string][]map[string]interface{}{}
	err := ReadTables(strings.NewReader(doc), chunk, func(table string, rows []map[string]interface{}) error {
		if rows == nil {
			order = append(order, table)
			got[table] = []map[string]interface{}{}
			return nil
		}
		if len(rows) > chunk {
			t.Fatalf("table %s: %d rows in one call, chunk %d", table, len(rows), chunk)
		}
		got[table] = append(got[table], rows...)
		return nil
	})
	if err != nil {
		t.Fatalf("ReadTables: %v\n%s", err, doc)
	}
	return order, got
}

// canonical compares documents by value, ignoring number representation.
func canonical(t *testing.T, data map[string][]map[string]interface{}) string {
	t.Helper()
	b, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	var v interface{}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(v)
	return string(out)
}

func TestWriters_MatchTheWholeDocumentEncoders(t *testing.T) {
	order, data := sample()
	wantYAML, err := yaml.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if got := write(t, "yaml", order, data, 3); got != string(wantYAML) {
		t.Fatalf("streamed YAML differs from yaml.Marshal:\n--- got\n%s\n--- want\n%s", got, wantYAML)
	}
	wantJSON, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if got := write(t, "json", order, data, 3); got != string(wantJSON)+"\n" {
		t.Fatalf("streamed JSON differs from json.MarshalIndent:\n--- got\n%s\n--- want\n%s", got, wantJSON)
	}
}

func TestReadTables_RoundTripsInAnyChunking(t *testing.T) {
	order, data := sample()
	for _, format := range []string{"yaml", "json"} {
		for _, chunk := range []int{1, 2, 5, 100} {
			t.Run(fmt.Sprintf("%s chunk %d", format, chunk), func(t *testing.T) {
				gotOrder, got := read(t, write(t, format, order, data, 3), chunk)
				if strings.Join(gotOrder, ",") != strings.Join(order, ",") {
					t.Fatalf("tables %v, want %v in document order", gotOrder, order)
				}
				if canonical(t, got) != canonical(t, data) {
					t.Fatalf("round trip changed the data:\n%s\n%s", canonical(t, got), canonical(t, data))
				}
			})
		}
	}
}

func TestReadTables_OtherYAMLLayoutsStillRead(t *testing.T) {
	_, data := sample()
	want := canonical(t, data)
	layouts := map[string]string{
		"indented lists": "empty_table: []\nposts:\n  - id: 1\n    title: \"- dash first\"\n    user_id: 3\nusers:\n",
		"flow style":     "",
	}
	indented, _ := yaml.MarshalWithOptions(data, yaml.IndentSequence(true))
	layouts["indented lists"] = string(indented)
	flow, _ := yaml.MarshalWithOptions(data, yaml.Flow(true))
	layouts["flow style"] = string(flow)
	layouts["document marker and comments"] = "---\n# generated\n" + mustYAML(t, data)
	for name, doc := range layouts {
		t.Run(name, func(t *testing.T) {
			_, got := read(t, doc, 2)
			if canonical(t, got) != want {
				t.Fatalf("read changed the data:\n%s\n%s\ndocument:\n%s", canonical(t, got), want, doc)
			}
		})
	}
}

func mustYAML(t *testing.T, v interface{}) string {
	t.Helper()
	b, err := yaml.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestReadTables_LargeIntegersKeepPrecisionInJSON(t *testing.T) {
	_, got := read(t, `{"t": [{"id": 9007199254740993, "ratio": 0.25}]}`, 10)
	if got["t"][0]["id"] != int64(9007199254740993) || got["t"][0]["ratio"] != 0.25 {
		t.Fatalf("row = %#v", got["t"][0])
	}
}

func TestReadTables_ReportsBrokenDocuments(t *testing.T) {
	for name, doc := range map[string]string{
		"json":           `{"t": [{"id": 1}`,
		"yaml item":      "t:\n- id: [1\n",
		"not a document": "just text",
	} {
		t.Run(name, func(t *testing.T) {
			err := ReadTables(strings.NewReader(doc), 10, func(string, []map[string]interface{}) error { return nil })
			if err == nil {
				t.Fatalf("no error for %q", doc)
			}
		})
	}
}

func TestSQLWriter_WritesRunnableInsertsInBatches(t *testing.T) {
	var buf bytes.Buffer
	w, _ := NewWriter(&buf, "sql", "mysql", 2)
	_ = w.Table("users")
	_ = w.Rows([]map[string]interface{}{{"id": 1, "name": `a'b\c`}, {"id": 2, "name": nil}, {"id": 3, "name": "z"}})
	_ = w.Close()
	want := "INSERT INTO `users` (`id`, `name`) VALUES (1, 'a''b\\\\c'), (2, NULL);\nINSERT INTO `users` (`id`, `name`) VALUES (3, 'z');\n"
	if buf.String() != want {
		t.Fatalf("got\n%s\nwant\n%s", buf.String(), want)
	}
}

func TestCSVWriter_OneHeaderPerTableAndEmptyNulls(t *testing.T) {
	var buf bytes.Buffer
	w, _ := NewWriter(&buf, "csv", "pgx", 0)
	_ = w.Table("a")
	_ = w.Rows([]map[string]interface{}{{"y": 1, "x": nil}})
	_ = w.Rows([]map[string]interface{}{{"y": 2, "x": "v"}})
	_ = w.Table("b")
	_ = w.Rows([]map[string]interface{}{{"k": "q,uote"}})
	_ = w.Close()
	want := "_table,x,y\na,,1\na,v,2\n_table,k\nb,\"q,uote\"\n"
	if buf.String() != want {
		t.Fatalf("got\n%q\nwant\n%q", buf.String(), want)
	}
}

func TestNewWriter_RejectsUnknownFormats(t *testing.T) {
	if _, err := NewWriter(&bytes.Buffer{}, "xml", "pgx", 1); err == nil {
		t.Fatal("xml must be refused")
	}
	var names []string
	for _, f := range []string{"yaml", "json", "sql", "csv"} {
		if _, err := NewWriter(&bytes.Buffer{}, f, "pgx", 1); err != nil {
			names = append(names, f)
		}
	}
	sort.Strings(names)
	if len(names) > 0 {
		t.Fatalf("formats refused: %v", names)
	}
}

// Hand-edited files use block scalars (with blank lines and lines that look
// like list items) and nested lists inside rows; none of it may split a row.
func TestReadTables_BlockScalarsAndNestedListsStayInsideTheirRow(t *testing.T) {
	doc := `notes:
- id: 1
  body: |
    first line
    - looks like an item

    after a blank line
  tags:
  - red
  - blue
- id: 2
  body: >-
    folded
    text
  tags: []
other:
  - id: 9
    items:
      - x
      - y
    text: |

      - leading blank and dash
`
	var want map[string][]map[string]interface{}
	if err := yaml.Unmarshal([]byte(doc), &want); err != nil {
		t.Fatal(err)
	}
	for _, chunk := range []int{1, 3} {
		order, got := read(t, doc, chunk)
		if strings.Join(order, ",") != "notes,other" || len(got["notes"]) != 2 || len(got["other"]) != 1 {
			t.Fatalf("chunk %d: tables %v with %d and %d rows", chunk, order, len(got["notes"]), len(got["other"]))
		}
		if canonical(t, got) != canonical(t, want) {
			t.Fatalf("chunk %d changed the data:\n%s\n%s", chunk, canonical(t, got), canonical(t, want))
		}
	}
}
