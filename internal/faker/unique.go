package faker

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"

	"github.com/AxeForging/seedstorm/internal/schema"
)

// maxUniqueRepairs bounds how often one row's free columns are regenerated to
// escape a multi-column UNIQUE collision before the row is dropped.
const maxUniqueRepairs = 50

func groupID(group []string) string { return strings.Join(group, ",") }

func groupLabel(group []string) string { return "UNIQUE (" + strings.Join(group, ", ") + ")" }

// uniqueTupleKey renders a row's values for one UNIQUE group, normalised like
// primary keys so generated and stored values compare equal.
func uniqueTupleKey(table schema.Table, group []string, row map[string]interface{}) string {
	parts := make([]string, len(group))
	for i, c := range group {
		parts[i] = keyValue(table.Columns[c].Type, row[c])
	}
	return strings.Join(parts, "\x1f")
}

// tupleHasNull reports whether any value of the group is NULL: SQL UNIQUE
// constraints never treat such tuples as duplicates.
func tupleHasNull(group []string, row map[string]interface{}) bool {
	for _, c := range group {
		if row[c] == nil {
			return true
		}
	}
	return false
}

// validGroups returns the table's UNIQUE groups whose columns all exist, plus a
// one-column group for every single-column UNIQUE that is not the primary key
// (rules can rewrite those into repeating values).
func validGroups(table schema.Table) [][]string {
	var out [][]string
	for _, name := range sortedColumnNames(table) {
		if col := table.Columns[name]; col.Unique && !col.PK {
			out = append(out, []string{name})
		}
	}
	for _, group := range table.Unique {
		ok := len(group) > 0
		for _, c := range group {
			if _, exists := table.Columns[c]; !exists {
				ok = false
			}
		}
		if ok {
			out = append(out, group)
		}
	}
	return out
}

// freeColumns are the group's columns a repair may regenerate: not keys, not
// generated, not rewritten by a rule, and backed by a random generator.
func freeColumns(table schema.Table, group []string, overrides map[string]ColumnOverride) []string {
	var free []string
	for _, c := range group {
		col := table.Columns[c]
		if col.PK || col.FK != "" || col.Generated || col.Faker == "" || col.Faker == uniqueSequenceFaker {
			continue
		}
		if _, ruled := overrides[c]; ruled {
			continue
		}
		free = append(free, c)
	}
	return free
}

// enforceUniqueGroups makes every multi-column UNIQUE group distinct across the
// generated rows and the tuples already stored. A colliding row gets its free
// columns regenerated; a row that stays in collision is dropped. It returns the
// kept rows and how many rows each group dropped.
func enforceUniqueGroups(rows []map[string]interface{}, table schema.Table, overrides map[string]ColumnOverride, stored map[string]*keySet) ([]map[string]interface{}, map[string]int) {
	groups := validGroups(table)
	if len(groups) == 0 {
		return rows, nil
	}
	taken := make([]*seen, len(groups))
	for i, group := range groups {
		taken[i] = newSeen(stored[groupID(group)])
	}
	conflict := func(row map[string]interface{}) int {
		for i, group := range groups {
			if !tupleHasNull(group, row) && taken[i].Has(uniqueTupleKey(table, group, row)) {
				return i
			}
		}
		return -1
	}

	dropped := map[string]int{}
	kept := make([]map[string]interface{}, 0, len(rows))
	for _, row := range rows {
		hit := conflict(row)
		for attempt := 0; hit >= 0 && attempt < maxUniqueRepairs; attempt++ {
			free := freeColumns(table, groups[hit], overrides)
			if len(free) == 0 {
				break
			}
			colName := free[rand.Intn(len(free))] //nolint:gosec // test data, not security
			v, err := generate(table.Columns[colName].Faker)
			if err != nil {
				break
			}
			if v, err = CoerceValue(table.Columns[colName], v); err != nil {
				break
			}
			row[colName] = v
			hit = conflict(row)
		}
		if hit >= 0 {
			dropped[groupLabel(groups[hit])]++
			continue
		}
		for i, group := range groups {
			if !tupleHasNull(group, row) {
				taken[i].Add(uniqueTupleKey(table, group, row))
			}
		}
		kept = append(kept, row)
	}
	return kept, dropped
}

// rebuildPKPool replaces the PK values generated for a table with those of the
// rows that survived, keeping the values that were preloaded from the database.
// Children then only reference rows that will actually be inserted.
func rebuildPKPool(generatedPKs map[string][]interface{}, tableName string, table schema.Table, preloaded int, rows []map[string]interface{}) {
	pool := append([]interface{}(nil), generatedPKs[tableName][:preloaded]...)
	pkCols := make([]string, 0)
	for name, col := range table.Columns {
		if col.PK && !col.Generated {
			pkCols = append(pkCols, name)
		}
	}
	sort.Strings(pkCols)
	for _, row := range rows {
		for _, c := range pkCols {
			pool = append(pool, row[c])
		}
	}
	generatedPKs[tableName] = pool
}

func droppedSummary(dropped map[string]int) string {
	labels := make([]string, 0, len(dropped))
	for label, n := range dropped {
		labels = append(labels, fmt.Sprintf("%s (%d rows)", label, n))
	}
	sort.Strings(labels)
	return "no more distinct values for " + strings.Join(labels, ", ")
}
