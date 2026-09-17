package web

import (
	"errors"

	"github.com/AxeForging/seedstorm/internal/runerr"
	"github.com/AxeForging/seedstorm/internal/safego"
	"github.com/AxeForging/seedstorm/internal/seeder"
)

// failureView describes where a run failed, for the page to show next to the
// error text.
func failureView(err error) map[string]any {
	out := map[string]any{"message": err.Error()}
	if e, ok := runerr.As(err); ok {
		out["side"], out["phase"], out["table"] = e.Side, string(e.Phase), e.Table
	}
	var p *safego.PanicError
	if errors.As(err, &p) {
		out["internal"] = true
		out["errorId"] = p.ID
	}
	return out
}

// partialSeedResult is what a failed seed or fill had written when it stopped:
// rows per table (0 for tables not reached), where it failed, and what to do.
func partialSeedResult(res seeder.SeedResult, order []string, err error) map[string]any {
	counts := make(map[string]int, len(order))
	var written, notWritten []string
	for _, t := range order {
		counts[t] = res.Counts[t]
		if res.Counts[t] > 0 {
			written = append(written, t)
		} else {
			notWritten = append(notWritten, t)
		}
	}
	next := "Fix the cause shown above, then run again."
	if e, ok := runerr.As(err); ok && e.Phase == runerr.PhaseWrite && len(written) > 0 {
		next = "Fill empty tables continues with the tables that were not written; seeding everything again adds rows to the ones that were."
	}
	var p *safego.PanicError
	if errors.As(err, &p) {
		next = "This is a bug in seedstorm: the server log has the details under error id " + p.ID + "."
	}
	return map[string]any{
		"partial":     true,
		"totalRows":   res.Total,
		"tableCounts": counts,
		"written":     written,
		"notWritten":  notWritten,
		"failure":     failureView(err),
		"nextStep":    next,
	}
}
