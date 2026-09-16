package graph

import (
	"fmt"
	"sort"
	"strings"

	"github.com/AxeForging/seedstorm/internal/schema"
)

// ApplyIgnore removes ignored tables from a run order and checks that every
// kept table can still be seeded without writing to them.
//
// A kept table with a NOT NULL FK to an ignored table is fine when that parent
// already has rows (the generator references them) and an error naming both
// tables when it is empty. Nullable FKs to an ignored parent are fine (seeded
// as NULL), as are self-references. populated is asked at most once per ignored
// parent that a kept table actually requires, so callers may back it with a
// database count. ignored is keyed by schema table name.
func ApplyIgnore(sc *schema.Schema, order []string, ignored map[string]bool, populated func(table string) (bool, error)) ([]string, error) {
	kept := make([]string, 0, len(order))
	for _, table := range order {
		if !ignored[table] {
			kept = append(kept, table)
		}
	}
	if len(ignored) == 0 || sc == nil {
		return kept, nil
	}

	type need struct{ child, column string }
	needs := make(map[string][]need)
	for _, table := range kept {
		for colName, col := range sc.Tables[table].Columns {
			if col.FK == "" || col.Nullable {
				continue
			}
			parent, _, ok := strings.Cut(col.FK, ".")
			if !ok || parent == table || !ignored[parent] {
				continue
			}
			needs[parent] = append(needs[parent], need{child: table, column: colName})
		}
	}

	parents := make([]string, 0, len(needs))
	for parent := range needs {
		parents = append(parents, parent)
	}
	sort.Strings(parents)
	var problems []string
	for _, parent := range parents {
		if populated == nil {
			return nil, fmt.Errorf("cannot check ignored table %q: no row count available", parent)
		}
		has, err := populated(parent)
		if err != nil {
			return nil, fmt.Errorf("count rows in ignored table %q: %w", parent, err)
		}
		if has {
			continue
		}
		list := needs[parent]
		sort.Slice(list, func(i, j int) bool {
			if list[i].child != list[j].child {
				return list[i].child < list[j].child
			}
			return list[i].column < list[j].column
		})
		for _, n := range list {
			problems = append(problems, fmt.Sprintf("%s.%s is NOT NULL and references ignored table %s, which is empty", n.child, n.column, parent))
		}
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("cannot seed without writing ignored tables: %s (seed the parent first, un-ignore it, or leave the child out)", strings.Join(problems, "; "))
	}
	return kept, nil
}
