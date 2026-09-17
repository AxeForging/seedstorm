package tui

import (
	"fmt"

	"github.com/AxeForging/seedstorm/internal/faker"
)

// Profile carries a compiled seed profile into the interactive flows, so the
// TUI generates exactly what `seed --profile` would without the flag.
type Profile struct {
	Name      string
	Overrides faker.Overrides
	Shapes    map[string]faker.Shape
	TableRows map[string]int
}

// Active reports whether a profile was supplied.
func (p Profile) Active() bool { return p.Name != "" }

// Summary is the one-line description shown on review screens.
func (p Profile) Summary() string {
	if !p.Active() {
		return ""
	}
	cols := 0
	for _, t := range p.Overrides {
		cols += len(t)
	}
	return fmt.Sprintf("%s · %d column rule(s) · %d table row count(s)", p.Name, cols, len(p.TableRows))
}

// mergeRows layers counts chosen in the TUI over the profile's counts.
func (p Profile) mergeRows(chosen map[string]int) map[string]int {
	if len(p.TableRows) == 0 {
		return chosen
	}
	out := make(map[string]int, len(p.TableRows)+len(chosen))
	for k, v := range p.TableRows {
		out[k] = v
	}
	for k, v := range chosen {
		if v > 0 {
			out[k] = v
		}
	}
	return out
}
