package compare

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/relations"
)

// SnapshotKind identifies a table-counts snapshot file.
const SnapshotKind = "seedstorm.table-counts"

// SnapshotVersion is the snapshot file format of counts only. Version 2 adds
// relationships and is written only when there are some, so older binaries
// keep reading counts-only files.
const SnapshotVersion = 1

// snapshotVersionRelationships is the first version with relationships.
const snapshotVersionRelationships = 2

// supportedSnapshotVersions lists every version ParseSnapshot reads.
var supportedSnapshotVersions = []int64{1, 2}

// Snapshot file formats accepted by EncodeSnapshot.
const (
	FormatJSON = "json"
	FormatYAML = "yaml"
)

// snapshotFields is the order fields are written in, and the set ParseSnapshot
// accepts at the top level.
var snapshotFields = []string{"kind", "version", "label", "dbType", "countMode", "takenAt", "tables", "relationships"}

// EncodeSnapshot writes s as a versioned table-counts file in format "json" or
// "yaml". Tables are sorted by name so two snapshots of one database diff line
// by line. Unknown row counts and sizes are written as db.UnknownCount (-1).
func EncodeSnapshot(s Snapshot, format string) ([]byte, error) {
	version := SnapshotVersion
	if len(s.Relationships) > 0 {
		version = snapshotVersionRelationships
	}
	doc := yaml.MapSlice{
		{Key: "kind", Value: SnapshotKind},
		{Key: "version", Value: version},
		{Key: "label", Value: s.Label},
		{Key: "dbType", Value: s.DBType},
		{Key: "countMode", Value: string(s.CountMode)},
	}
	if !s.TakenAt.IsZero() {
		doc = append(doc, yaml.MapItem{Key: "takenAt", Value: s.TakenAt.UTC().Format(time.RFC3339Nano)})
	}
	tables := yaml.MapSlice{}
	for _, name := range sortedNames(s.Tables) {
		st := s.Tables[name]
		entry := yaml.MapSlice{{Key: "rows", Value: st.Rows}}
		if st.Estimated {
			entry = append(entry, yaml.MapItem{Key: "estimated", Value: true})
		}
		entry = append(entry, yaml.MapItem{Key: "bytes", Value: st.Bytes})
		if len(st.Columns) > 0 {
			entry = append(entry, yaml.MapItem{Key: "columns", Value: st.Columns})
		}
		tables = append(tables, yaml.MapItem{Key: name, Value: entry})
	}
	doc = append(doc, yaml.MapItem{Key: "tables", Value: tables})
	if len(s.Relationships) > 0 {
		doc = append(doc, yaml.MapItem{Key: "relationships", Value: s.Relationships})
	}

	switch strings.ToLower(strings.TrimSpace(format)) {
	case FormatYAML, "yml":
		return yaml.Marshal(doc)
	case FormatJSON:
		var buf bytes.Buffer
		writeJSONMapSlice(&buf, doc, "")
		buf.WriteByte('\n')
		return buf.Bytes(), nil
	}
	return nil, fmt.Errorf("unknown snapshot format %q (use json or yaml)", format)
}

// writeJSONMapSlice renders an ordered document as indented JSON; encoding/json
// would sort the top-level keys alphabetically and hide kind/version.
func writeJSONMapSlice(buf *bytes.Buffer, v any, indent string) {
	switch val := v.(type) {
	case yaml.MapSlice:
		if len(val) == 0 {
			buf.WriteString("{}")
			return
		}
		buf.WriteString("{\n")
		for i, item := range val {
			key, _ := json.Marshal(fmt.Sprint(item.Key))
			buf.WriteString(indent + "  ")
			buf.Write(key)
			buf.WriteString(": ")
			writeJSONMapSlice(buf, item.Value, indent+"  ")
			if i < len(val)-1 {
				buf.WriteByte(',')
			}
			buf.WriteByte('\n')
		}
		buf.WriteString(indent + "}")
	default:
		b, _ := json.Marshal(val) // strings, numbers, bools and []string cannot fail
		buf.Write(b)
	}
}

// ParseSnapshot reads a table-counts snapshot written as JSON or YAML.
//
// The full form is what EncodeSnapshot writes:
//
//	kind: seedstorm.table-counts
//	version: 1
//	label: app@db.example:5432
//	dbType: pgx
//	countMode: exact
//	takenAt: "2026-09-16T10:00:00Z"
//	tables:
//	  users: {rows: 1200, bytes: 65536, columns: [id, email]}
//	  orders: {rows: 5000, estimated: true, bytes: -1}
//
// A minimal hand-written form is also accepted, mapping each table straight to
// its row count (kind and version may be omitted here; sizes become unknown):
//
//	tables:
//	  users: 1200
//	  orders: 5000
//
// Row counts and sizes must be >= 0, or db.UnknownCount (-1) for unknown.
// Errors are written for people who pasted the wrong file.
func ParseSnapshot(data []byte) (Snapshot, error) {
	var snap Snapshot
	if len(bytes.TrimSpace(data)) == 0 {
		return snap, errors.New("snapshot is empty: expected a table-counts file (JSON or YAML) with a tables section")
	}
	var raw any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		msg := yaml.FormatError(err, false, false)
		if strings.Contains(msg, "already defined") {
			return snap, fmt.Errorf("snapshot lists the same name twice: %s", msg)
		}
		return snap, fmt.Errorf("snapshot is not valid JSON or YAML: %s", msg)
	}
	top, ok := raw.(map[string]any)
	if !ok {
		return snap, fmt.Errorf("not a table-counts snapshot: expected an object with kind, version and tables, got %s", describe(raw))
	}

	if _, isProfile := top["rules"]; isProfile {
		return snap, errors.New("this looks like a seed profile, not a table-counts snapshot (create one with `seedstorm snapshot` or export it from a compare)")
	}
	var unexpected []string
	for key := range top {
		if !slices.Contains(snapshotFields, key) {
			unexpected = append(unexpected, fmt.Sprintf("%q", key))
		}
	}
	if len(unexpected) > 0 {
		sort.Strings(unexpected)
		return snap, fmt.Errorf("not a table-counts snapshot: unexpected field(s) %s (allowed: %s)", strings.Join(unexpected, ", "), strings.Join(snapshotFields, ", "))
	}

	kindValue, hasKind := top["kind"]
	minimal := !hasKind
	if hasKind {
		kind, _ := kindValue.(string)
		if kind != SnapshotKind {
			return snap, fmt.Errorf("not a table-counts snapshot: kind is %s, want %q", describe(kindValue), SnapshotKind)
		}
		version, present := top["version"]
		if !present {
			return snap, fmt.Errorf("snapshot has no version (supported: %s)", versionList())
		}
		n, ok := toInt64(version)
		if !ok || !slices.Contains(supportedSnapshotVersions, n) {
			return snap, fmt.Errorf("unsupported snapshot version %v (supported: %s)", version, versionList())
		}
		if _, has := top["relationships"]; has && n < snapshotVersionRelationships {
			return snap, fmt.Errorf("relationships need version %d of the snapshot format (this file says %d)", snapshotVersionRelationships, n)
		}
	} else if _, present := top["version"]; present {
		return snap, fmt.Errorf("snapshot has a version but no kind: add kind: %s", SnapshotKind)
	}

	if snap.Label, ok = optionalString(top, "label"); !ok {
		return snap, errors.New("snapshot label must be text")
	}
	dbType, ok := optionalString(top, "dbType")
	if !ok {
		return snap, errors.New("snapshot dbType must be text")
	}
	if snap.DBType, ok = snapshotDBType(dbType); !ok {
		return snap, fmt.Errorf("snapshot dbType %q is not supported (use postgres or mysql)", dbType)
	}
	mode, ok := optionalString(top, "countMode")
	if !ok {
		return snap, errors.New("snapshot countMode must be text")
	}
	if mode != "" {
		m, err := ParseCountMode(mode)
		if err != nil {
			return snap, fmt.Errorf("snapshot countMode: %w", err)
		}
		snap.CountMode = m
	}
	switch v := top["takenAt"].(type) {
	case nil:
	case time.Time:
		snap.TakenAt = v.UTC()
	case string:
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return snap, fmt.Errorf("snapshot takenAt %q is not an RFC 3339 timestamp (like 2026-09-16T10:00:00Z)", v)
		}
		snap.TakenAt = t.UTC()
	default:
		return snap, fmt.Errorf("snapshot takenAt must be a timestamp, got %s", describe(v))
	}

	if raw, has := top["relationships"]; has {
		if minimal {
			return snap, fmt.Errorf("relationships need kind: %s and version: %d at the top", SnapshotKind, snapshotVersionRelationships)
		}
		shapes, err := parseRelationships(raw)
		if err != nil {
			return snap, err
		}
		snap.Relationships = shapes
	}

	tablesValue, present := top["tables"]
	if !present || tablesValue == nil {
		return snap, errors.New("snapshot has no tables")
	}
	tables, ok := tablesValue.(map[string]any)
	if !ok {
		return snap, fmt.Errorf("snapshot tables must map table names to row counts, got %s", describe(tablesValue))
	}
	if len(tables) == 0 {
		return snap, errors.New("snapshot has no tables")
	}
	snap.Tables = make(map[string]TableStat, len(tables))
	names := make([]string, 0, len(tables))
	for name := range tables {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		value := tables[name]
		if strings.TrimSpace(name) == "" {
			return snap, errors.New("snapshot has a table with an empty name")
		}
		st, err := parseTableStat(value, minimal)
		if err != nil {
			return snap, fmt.Errorf("table %q: %w", name, err)
		}
		snap.Tables[name] = st
	}
	return snap, nil
}

func parseTableStat(value any, minimal bool) (TableStat, error) {
	if rows, ok := toInt64(value); ok {
		if err := checkCount("row count", rows); err != nil {
			return TableStat{}, err
		}
		return TableStat{Rows: rows, Bytes: db.UnknownCount}, nil
	}
	fields, ok := value.(map[string]any)
	if !ok {
		return TableStat{}, fmt.Errorf("expected a whole-number row count, got %s", describe(value))
	}
	if minimal {
		return TableStat{}, fmt.Errorf("expected a row count like `users: 1200`; per-table details need kind: %s and version: %d at the top", SnapshotKind, SnapshotVersion)
	}
	for key := range fields {
		switch key {
		case "rows", "estimated", "bytes", "columns":
		default:
			return TableStat{}, fmt.Errorf("unexpected field %q (allowed: rows, estimated, bytes, columns)", key)
		}
	}
	st := TableStat{Rows: db.UnknownCount, Bytes: db.UnknownCount}
	rowsValue, present := fields["rows"]
	if !present {
		return st, errors.New("missing rows")
	}
	if st.Rows, ok = toInt64(rowsValue); !ok {
		return st, fmt.Errorf("rows must be a whole number, got %s", describe(rowsValue))
	}
	if err := checkCount("row count", st.Rows); err != nil {
		return st, err
	}
	if v, present := fields["bytes"]; present && v != nil {
		if st.Bytes, ok = toInt64(v); !ok {
			return st, fmt.Errorf("bytes must be a whole number, got %s", describe(v))
		}
		if err := checkCount("size", st.Bytes); err != nil {
			return st, err
		}
	}
	if v, present := fields["estimated"]; present && v != nil {
		if st.Estimated, ok = v.(bool); !ok {
			return st, fmt.Errorf("estimated must be true or false, got %s", describe(v))
		}
	}
	if v, present := fields["columns"]; present && v != nil {
		list, ok := v.([]any)
		if !ok {
			return st, fmt.Errorf("columns must be a list of names, got %s", describe(v))
		}
		for _, c := range list {
			name, ok := c.(string)
			if !ok || name == "" {
				return st, fmt.Errorf("columns must be a list of names, got %s", describe(c))
			}
			st.Columns = append(st.Columns, name)
		}
	}
	return st, nil
}

func checkCount(what string, n int64) error {
	if n < 0 && n != db.UnknownCount {
		return fmt.Errorf("%s %d is negative (use %d for unknown)", what, n, db.UnknownCount)
	}
	return nil
}

// toInt64 converts a decoded YAML/JSON number without losing precision.
func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case uint64:
		if n > math.MaxInt64 {
			return 0, false
		}
		return int64(n), true
	case float64:
		if n != math.Trunc(n) || n > math.MaxInt64 || n < math.MinInt64 {
			return 0, false
		}
		return int64(n), true
	}
	return 0, false
}

func optionalString(m map[string]any, key string) (string, bool) {
	v, present := m[key]
	if !present || v == nil {
		return "", true
	}
	s, ok := v.(string)
	return s, ok
}

// snapshotDBType maps a user-written engine name to the driver name snapshots use.
func snapshotDBType(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return "", true
	case "pgx", "postgres", "postgresql", "pg":
		return "pgx", true
	case "mysql", "mariadb":
		return "mysql", true
	}
	return "", false
}

func describe(v any) string {
	switch val := v.(type) {
	case nil:
		return "nothing"
	case string:
		return fmt.Sprintf("%q", val)
	case []any:
		return "a list"
	case map[string]any:
		return "an object"
	case bool:
		return fmt.Sprintf("%t", val)
	case int, int64, uint64, float64:
		return fmt.Sprintf("the number %v", val)
	}
	return fmt.Sprintf("%v", v)
}

func versionList() string {
	parts := make([]string, len(supportedSnapshotVersions))
	for i, v := range supportedSnapshotVersions {
		parts[i] = fmt.Sprint(v)
	}
	return strings.Join(parts, ", ")
}

// parseRelationships reads the relationships section through JSON, which the
// shape type describes field by field.
func parseRelationships(raw any) ([]relations.Shape, error) {
	if raw == nil {
		return nil, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("snapshot relationships: %w", err)
	}
	var shapes []relations.Shape
	if err := json.Unmarshal(data, &shapes); err != nil {
		return nil, fmt.Errorf("snapshot relationships must be a list of foreign-key shapes: %w", err)
	}
	for i, s := range shapes {
		if s.Child == "" || s.Column == "" || s.Parent == "" {
			return nil, fmt.Errorf("snapshot relationship %d needs child, column and parent", i+1)
		}
	}
	return shapes, nil
}
