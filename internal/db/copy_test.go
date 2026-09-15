package db

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestWriteCSVRows_NullsEmptyStringsAndSpecialValues(t *testing.T) {
	ts := time.Date(2024, 5, 6, 7, 8, 9, 123456000, time.FixedZone("x", 3600))
	rows := []map[string]interface{}{
		{"a": nil, "b": "", "c": `say "hi", then
leave`, "d": true, "e": 42, "f": 1.5, "g": ts, "h": []byte{0xde, 0xad}},
	}
	var buf bytes.Buffer
	if err := writeCSVRows(&buf, []string{"a", "b", "c", "d", "e", "f", "g", "h"}, rows); err != nil {
		t.Fatal(err)
	}
	want := `,"","say ""hi"", then` + "\n" + `leave","t","42","1.5","2024-05-06 06:08:09.123456+00","\xdead"` + "\n"
	if got := buf.String(); got != want {
		t.Fatalf("csv =\n%q\nwant\n%q", got, want)
	}
}

func TestCopyColumns_SortedAndQuoted(t *testing.T) {
	cols, stmt := copyStatement("Order Items", []map[string]interface{}{{"qty": 1, "id": 2}})
	if strings.Join(cols, ",") != "id,qty" {
		t.Fatalf("cols = %v", cols)
	}
	if stmt != `COPY "Order Items" ("id", "qty") FROM STDIN WITH (FORMAT csv)` {
		t.Fatalf("stmt = %s", stmt)
	}
}
