package dataio

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
)

// DefaultReadChunk is how many rows a reader hands over at once.
const DefaultReadChunk = 5_000

// ReadTables streams a data document — a map of table → list of rows, in JSON
// or YAML — calling fn once with nil rows when a table starts and then with at
// most chunk rows at a time. JSON is decoded token by token. YAML in block
// style (what generate writes) is split at list items and parsed a chunk at a
// time; any other YAML layout is parsed whole.
func ReadTables(r io.Reader, chunk int, fn func(table string, rows []map[string]interface{}) error) error {
	if chunk <= 0 {
		chunk = DefaultReadChunk
	}
	br := bufio.NewReaderSize(r, 64<<10)
	first, err := firstNonSpace(br)
	if err == io.EOF {
		return nil
	}
	if err != nil {
		return err
	}
	if first != '{' {
		return readYAML(br, chunk, fn)
	}
	// A document starting with '{' is JSON, or YAML in flow style. Keep what
	// the JSON decoder reads until it hands over anything, so a YAML document
	// can still be parsed whole.
	rec := &recorder{r: br, on: true}
	called := false
	err = readJSON(rec, chunk, func(table string, rows []map[string]interface{}) error {
		called, rec.on, rec.buf = true, false, nil
		return fn(table, rows)
	})
	if err == nil || called {
		return err
	}
	rest, readErr := io.ReadAll(br)
	if readErr != nil {
		return readErr
	}
	return parseWholeYAML(append(rec.buf, rest...), chunk, fn)
}

// recorder copies what is read through it while on.
type recorder struct {
	r   io.Reader
	on  bool
	buf []byte
}

func (r *recorder) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if r.on {
		r.buf = append(r.buf, p[:n]...)
	}
	return n, err
}

func firstNonSpace(br *bufio.Reader) (byte, error) {
	for {
		b, err := br.ReadByte()
		if err != nil {
			return 0, err
		}
		if b != ' ' && b != '\t' && b != '\n' && b != '\r' && b != 0xEF && b != 0xBB && b != 0xBF {
			return b, br.UnreadByte()
		}
	}
}

func readJSON(br io.Reader, chunk int, fn func(string, []map[string]interface{}) error) error {
	dec := json.NewDecoder(br)
	dec.UseNumber()
	if err := expectDelim(dec, '{'); err != nil {
		return err
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("parse data json: %w", err)
		}
		table, ok := tok.(string)
		if !ok {
			return fmt.Errorf("parse data json: expected a table name, got %v", tok)
		}
		if err := fn(table, nil); err != nil {
			return err
		}
		if err := expectDelim(dec, '['); err != nil {
			return fmt.Errorf("table %s: %w", table, err)
		}
		buf := make([]map[string]interface{}, 0, min(chunk, 1024))
		for dec.More() {
			var row map[string]interface{}
			if err := dec.Decode(&row); err != nil {
				return fmt.Errorf("parse data json: table %s: %w", table, err)
			}
			buf = append(buf, normalizeJSONRow(row))
			if len(buf) >= chunk {
				if err := fn(table, buf); err != nil {
					return err
				}
				buf = make([]map[string]interface{}, 0, len(buf))
			}
		}
		if err := expectDelim(dec, ']'); err != nil {
			return fmt.Errorf("table %s: %w", table, err)
		}
		if len(buf) > 0 {
			if err := fn(table, buf); err != nil {
				return err
			}
		}
	}
	return expectDelim(dec, '}')
}

func expectDelim(dec *json.Decoder, want json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return fmt.Errorf("parse data json: %w", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != want {
		return fmt.Errorf("parse data json: expected %q, got %v", want, tok)
	}
	return nil
}

// normalizeJSONRow turns json.Number into int64 when it is an integer (so ids
// keep full precision) and float64 otherwise.
func normalizeJSONRow(row map[string]interface{}) map[string]interface{} {
	for k, v := range row {
		n, ok := v.(json.Number)
		if !ok {
			continue
		}
		if i, err := strconv.ParseInt(string(n), 10, 64); err == nil {
			row[k] = i
		} else if f, err := n.Float64(); err == nil {
			row[k] = f
		}
	}
	return row
}

var errWholeDocument = errors.New("layout needs a whole-document parse")

// readYAML splits block-style YAML at top-level keys and at list items. Lines
// belonging to an item (deeper indentation, blank lines inside block scalars)
// stay with it, and each chunk of items is parsed as one YAML list. A layout it
// cannot split safely falls back to parsing the whole document, which is only
// possible before any rows were handed over.
func readYAML(br *bufio.Reader, chunk int, fn func(string, []map[string]interface{}) error) error {
	var consumed bytes.Buffer // kept only until fn is first called
	emitted := false
	call := func(table string, rows []map[string]interface{}) error {
		emitted, consumed = true, bytes.Buffer{}
		return fn(table, rows)
	}
	table, inTable := "", false
	itemIndent := -1
	var block bytes.Buffer // lines of the current items
	items := 0

	flush := func() error {
		if items == 0 {
			block.Reset()
			return nil
		}
		var rows []map[string]interface{}
		if err := yaml.Unmarshal(block.Bytes(), &rows); err != nil {
			return fmt.Errorf("parse data yaml: table %s: %w", table, err)
		}
		block.Reset()
		items = 0
		return call(table, rows)
	}

	for lineNo := 1; ; lineNo++ {
		line, readErr := br.ReadString('\n')
		if readErr != nil && readErr != io.EOF {
			return readErr
		}
		if line == "" && readErr == io.EOF {
			break
		}
		if !emitted {
			consumed.WriteString(line)
		}
		trimmed := strings.TrimRight(line, "\r\n")
		stripped := strings.TrimLeft(trimmed, " ")
		indent := len(trimmed) - len(stripped)

		switch {
		case stripped == "" || (indent == 0 && (strings.HasPrefix(stripped, "#") || stripped == "---" || stripped == "...")):
			if items > 0 {
				block.WriteString(line)
			}
		case indent == 0 && !isItemStart(stripped):
			// A top-level key: the previous table is complete.
			if err := flush(); err != nil {
				return err
			}
			name, empty, err := parseTableKey(trimmed)
			if err != nil {
				return wholeDocument(err, emitted, &consumed, br, chunk, fn)
			}
			table, inTable, itemIndent = name, true, -1
			if err := call(table, nil); err != nil {
				return err
			}
			if empty {
				inTable = false
			}
		case inTable && isItemStart(stripped) && (itemIndent < 0 || indent == itemIndent):
			if itemIndent < 0 {
				itemIndent = indent
			}
			if items >= chunk {
				if err := flush(); err != nil {
					return err
				}
			}
			items++
			block.WriteString(line[itemIndent:])
		case inTable && items > 0 && indent > itemIndent:
			block.WriteString(line[min(itemIndent, len(line)):])
		default:
			return wholeDocument(fmt.Errorf("line %d: unexpected %q", lineNo, trimmed), emitted, &consumed, br, chunk, fn)
		}
		if readErr == io.EOF {
			break
		}
	}
	return flush()
}

func isItemStart(s string) bool { return s == "-" || strings.HasPrefix(s, "- ") }

// parseTableKey reads "name:" (a list follows) or "name: []" (an empty table).
// Anything else, like a flow-style list on the same line, is not streamable.
func parseTableKey(line string) (name string, empty bool, err error) {
	var m yaml.MapSlice
	if err := yaml.Unmarshal([]byte(line), &m); err != nil || len(m) != 1 {
		return "", false, errWholeDocument
	}
	name = fmt.Sprint(m[0].Key)
	switch v := m[0].Value.(type) {
	case nil:
		return name, false, nil
	case []interface{}:
		if len(v) == 0 {
			return name, true, nil
		}
	}
	return "", false, errWholeDocument
}

// wholeDocument parses what was read plus the rest in one go, for layouts the
// line splitter does not handle. Once fn was called that is no longer possible
// and the original error is returned.
func wholeDocument(cause error, emitted bool, consumed *bytes.Buffer, br *bufio.Reader, chunk int, fn func(string, []map[string]interface{}) error) error {
	if emitted {
		return fmt.Errorf("parse data yaml: %w (write each table as a block list to stream it)", cause)
	}
	rest, err := io.ReadAll(br)
	if err != nil {
		return err
	}
	return parseWholeYAML(append(consumed.Bytes(), rest...), chunk, fn)
}

func parseWholeYAML(doc []byte, chunk int, fn func(string, []map[string]interface{}) error) error {
	var tables yaml.MapSlice
	if err := yaml.Unmarshal(doc, &tables); err != nil {
		return fmt.Errorf("parse data yaml: %w", err)
	}
	for _, item := range tables {
		table := fmt.Sprint(item.Key)
		var rows []map[string]interface{}
		b, err := yaml.Marshal(item.Value)
		if err != nil {
			return err
		}
		if err := yaml.Unmarshal(b, &rows); err != nil {
			return fmt.Errorf("parse data yaml: table %s: %w", table, err)
		}
		if err := fn(table, nil); err != nil {
			return err
		}
		for i := 0; i < len(rows); i += chunk {
			if err := fn(table, rows[i:min(i+chunk, len(rows))]); err != nil {
				return err
			}
		}
	}
	return nil
}
