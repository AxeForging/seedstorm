package cli

import (
	"fmt"
	"strconv"
	"strings"
)

func parseTableRows(values []string) (map[string]int, error) {
	if len(values) == 0 {
		return nil, nil
	}
	rows := make(map[string]int)
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			name, countText, ok := strings.Cut(part, "=")
			if !ok {
				return nil, fmt.Errorf("invalid table row override %q, expected table=rows", part)
			}
			name = strings.TrimSpace(name)
			countText = strings.TrimSpace(countText)
			if name == "" || countText == "" {
				return nil, fmt.Errorf("invalid table row override %q, expected table=rows", part)
			}
			count, err := strconv.Atoi(countText)
			if err != nil || count < 1 {
				return nil, fmt.Errorf("invalid row count for %s: %q", name, countText)
			}
			rows[name] = count
		}
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return rows, nil
}
