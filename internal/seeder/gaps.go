package seeder

// KnownEmpty reports whether counts says a table has no rows. A table missing
// from counts (its count failed) is unknown, never empty.
func KnownEmpty(counts map[string]int64, table string) bool {
	n, ok := counts[table]
	return ok && n == 0
}

// GapTables returns the tables of order known to be empty, in order. With only
// set, just those tables are considered.
func GapTables(order []string, counts map[string]int64, only []string) []string {
	var allowed map[string]bool
	if len(only) > 0 {
		allowed = make(map[string]bool, len(only))
		for _, t := range only {
			allowed[t] = true
		}
	}
	var gaps []string
	for _, t := range order {
		if allowed != nil && !allowed[t] {
			continue
		}
		if KnownEmpty(counts, t) {
			gaps = append(gaps, t)
		}
	}
	return gaps
}
