//go:build faultinject

package faultinject

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
)

type fault struct{ point, table, mode string }

var (
	once   sync.Once
	faults []fault
)

// Hit fails the named step when SEEDSTORM_FAULT asks for it. The variable
// holds comma-separated point:table:mode entries (table * matches any);
// mode is panic, error, or hang (block until ctx ends).
func Hit(ctx context.Context, point, table string) error {
	once.Do(func() {
		for _, entry := range strings.Split(os.Getenv("SEEDSTORM_FAULT"), ",") {
			parts := strings.Split(strings.TrimSpace(entry), ":")
			if len(parts) == 3 {
				faults = append(faults, fault{parts[0], parts[1], parts[2]})
			}
		}
	})
	for _, f := range faults {
		if f.point != point || (f.table != "*" && f.table != table) {
			continue
		}
		switch f.mode {
		case "panic":
			panic(fmt.Sprintf("injected panic at %s %s", point, table))
		case "error":
			return fmt.Errorf("injected failure at %s %s", point, table)
		case "hang":
			<-ctx.Done()
			return ctx.Err()
		}
	}
	return nil
}
