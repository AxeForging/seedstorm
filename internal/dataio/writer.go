// Package dataio streams generated data documents: a map of table → rows in
// YAML, JSON, SQL or CSV. Writers take rows chunk by chunk and readers hand
// them back the same way, so generate and export never hold a whole data set.
package dataio

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/goccy/go-yaml"
)

// Writer receives a document table by table. Table starts a table (ending the
// previous one), Rows appends rows to it, Close finishes the document. Close
// does not close the underlying io.Writer.
type Writer interface {
	Table(name string) error
	Rows(rows []map[string]interface{}) error
	Close() error
}

// NewWriter returns a streaming writer for format: yaml, json, sql or csv.
// batchSize is the most rows per SQL INSERT (1 writes one statement per row).
func NewWriter(w io.Writer, format, dbType string, batchSize int) (Writer, error) {
	bw := bufio.NewWriterSize(w, 64<<10)
	switch strings.ToLower(format) {
	case "yaml", "yml", "":
		return &yamlWriter{w: bw}, nil
	case "json":
		return &jsonWriter{w: bw}, nil
	case "sql":
		if batchSize < 1 {
			batchSize = 1
		}
		return &sqlWriter{w: bw, dbType: dbType, batch: batchSize}, nil
	case "csv":
		return &csvWriter{bw: bw, w: csv.NewWriter(bw)}, nil
	}
	return nil, fmt.Errorf("unknown format %q (use yaml, json, sql or csv)", format)
}

// yamlWriter writes block style, the same layout yaml.Marshal gives the whole
// map: "table:" then "- column: value" items at column zero.
type yamlWriter struct {
	w       *bufio.Writer
	table   string
	started bool // the table's header has been written
	open    bool // a table is in progress
}

func (y *yamlWriter) Table(name string) error {
	if err := y.end(); err != nil {
		return err
	}
	y.table, y.started, y.open = name, false, true
	return nil
}

func (y *yamlWriter) Rows(rows []map[string]interface{}) error {
	if len(rows) == 0 {
		return nil
	}
	if !y.started {
		if _, err := y.w.WriteString(yamlKey(y.table) + ":\n"); err != nil {
			return err
		}
		y.started = true
	}
	// goccy builds a node tree per Marshal call, so large chunks are encoded a
	// slice at a time to keep that tree small.
	for i := 0; i < len(rows); i += yamlSlice {
		b, err := yaml.Marshal(rows[i:min(i+yamlSlice, len(rows))])
		if err != nil {
			return fmt.Errorf("YAML marshal failed: %w", err)
		}
		if _, err := y.w.Write(b); err != nil {
			return err
		}
	}
	return nil
}

// yamlSlice is how many rows one YAML Marshal call encodes.
const yamlSlice = 500

func (y *yamlWriter) end() error {
	if y.open && !y.started {
		if _, err := y.w.WriteString(yamlKey(y.table) + ": []\n"); err != nil {
			return err
		}
	}
	y.open = false
	return nil
}

func (y *yamlWriter) Close() error {
	if err := y.end(); err != nil {
		return err
	}
	return y.w.Flush()
}

// yamlKey renders a table name as a YAML key, quoted only when it must be.
func yamlKey(name string) string {
	b, err := yaml.Marshal(name)
	if err != nil {
		return fmt.Sprintf("%q", name)
	}
	return strings.TrimSpace(string(b))
}

// jsonWriter writes the layout json.MarshalIndent(data, "", "  ") gives.
type jsonWriter struct {
	w      *bufio.Writer
	tables int
	rows   int
	open   bool
}

func (j *jsonWriter) Table(name string) error {
	if err := j.end(); err != nil {
		return err
	}
	key, err := json.Marshal(name)
	if err != nil {
		return err
	}
	prefix := "{\n  "
	if j.tables > 0 {
		prefix = ",\n  "
	}
	if _, err := j.w.WriteString(prefix + string(key) + ": ["); err != nil {
		return err
	}
	j.tables++
	j.rows, j.open = 0, true
	return nil
}

func (j *jsonWriter) Rows(rows []map[string]interface{}) error {
	for _, row := range rows {
		b, err := json.MarshalIndent(row, "    ", "  ")
		if err != nil {
			return fmt.Errorf("JSON marshal failed: %w", err)
		}
		sep := "\n    "
		if j.rows > 0 {
			sep = ",\n    "
		}
		if _, err := j.w.WriteString(sep); err != nil {
			return err
		}
		if _, err := j.w.Write(b); err != nil {
			return err
		}
		j.rows++
	}
	return nil
}

func (j *jsonWriter) end() error {
	if !j.open {
		return nil
	}
	j.open = false
	closing := "]"
	if j.rows > 0 {
		closing = "\n  ]"
	}
	_, err := j.w.WriteString(closing)
	return err
}

func (j *jsonWriter) Close() error {
	if err := j.end(); err != nil {
		return err
	}
	tail := "\n}\n"
	if j.tables == 0 {
		tail = "{}\n"
	}
	if _, err := j.w.WriteString(tail); err != nil {
		return err
	}
	return j.w.Flush()
}

// sqlWriter writes INSERT statements with literal values.
type sqlWriter struct {
	w      *bufio.Writer
	dbType string
	table  string
	batch  int
}

func (s *sqlWriter) Table(name string) error { s.table = name; return nil }

func (s *sqlWriter) Rows(rows []map[string]interface{}) error {
	for _, batch := range db.SplitBatches(rows, s.batch) {
		if _, err := s.w.WriteString(db.RenderInsert(s.table, batch, s.dbType) + "\n"); err != nil {
			return err
		}
	}
	return nil
}

func (s *sqlWriter) Close() error { return s.w.Flush() }

// csvWriter writes one header per table ("_table" then the sorted columns of
// the table's first rows) followed by its rows; NULL is an empty field.
type csvWriter struct {
	bw      *bufio.Writer
	w       *csv.Writer
	table   string
	headers []string
}

func (c *csvWriter) Table(name string) error {
	c.table, c.headers = name, nil
	return nil
}

func (c *csvWriter) Rows(rows []map[string]interface{}) error {
	if len(rows) == 0 {
		return nil
	}
	if c.headers == nil {
		cols := make([]string, 0, len(rows[0]))
		for k := range rows[0] {
			cols = append(cols, k)
		}
		sort.Strings(cols)
		c.headers = cols
		if err := c.w.Write(append([]string{"_table"}, cols...)); err != nil {
			return err
		}
	}
	record := make([]string, len(c.headers)+1)
	for _, row := range rows {
		record[0] = c.table
		for i, k := range c.headers {
			if v := row[k]; v != nil {
				record[i+1] = fmt.Sprintf("%v", v)
			} else {
				record[i+1] = ""
			}
		}
		if err := c.w.Write(record); err != nil {
			return err
		}
	}
	return nil
}

func (c *csvWriter) Close() error {
	c.w.Flush()
	if err := c.w.Error(); err != nil {
		return err
	}
	return c.bw.Flush()
}
