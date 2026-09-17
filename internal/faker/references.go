package faker

import (
	"sort"

	"github.com/AxeForging/seedstorm/internal/schema"
)

// Foreign keys usually reference their parent's single primary key, whose
// values the parent's key pool (keyed by table name) already holds. An FK can
// also reference a UNIQUE column, or one column of a composite key; those
// values live in a pool of their own, keyed by the FK target "table.column".

// referencedColumns lists, per parent table, the columns some FK references
// that are not the parent's single primary key, sorted.
func referencedColumns(sc *schema.Schema) map[string][]string {
	seen := map[string]map[string]bool{}
	for _, table := range sc.Tables {
		for _, col := range table.Columns {
			parent, refCol := splitFK(col.FK)
			if parent == "" || refCol == "" {
				continue
			}
			parentTable, ok := sc.Tables[parent]
			if !ok || isSolePK(parentTable, refCol) {
				continue
			}
			if _, ok := parentTable.Columns[refCol]; !ok {
				continue
			}
			if seen[parent] == nil {
				seen[parent] = map[string]bool{}
			}
			seen[parent][refCol] = true
		}
	}
	out := make(map[string][]string, len(seen))
	for parent, cols := range seen {
		for c := range cols {
			out[parent] = append(out[parent], c)
		}
		sort.Strings(out[parent])
	}
	return out
}

func isSolePK(table schema.Table, colName string) bool {
	pks := sortedPKColumns(table)
	return len(pks) == 1 && pks[0] == colName
}

// referencePoolKey is the pool key of a referenced non-key column.
func referencePoolKey(table, col string) string { return table + "." + col }

// poolFor returns the values an FK may take: the referenced column's own pool
// when it has one, else the parent's key pool.
func poolFor(generatedPKs map[string][]interface{}, fk string) []interface{} {
	if pool, ok := generatedPKs[fk]; ok {
		return pool
	}
	parent, _ := splitFK(fk)
	return generatedPKs[parent]
}

// recordReferencedValues adds final rows' values of referenced columns to their
// pools, keeping each pool bounded.
func (g *Stream) recordReferencedValues(tableName string, rows []map[string]interface{}) {
	for _, col := range g.refCols[tableName] {
		key := referencePoolKey(tableName, col)
		pool := g.pks[key]
		for _, row := range rows {
			if v := row[col]; v != nil {
				pool = append(pool, v)
			}
		}
		g.pks[key] = capPool(pool, poolLimit, g.gen.rnd)
	}
}
