package seeder

import (
	"fmt"
	"io"
	"text/tabwriter"
)

// RenderResult writes per-table outcomes, listing problems with their reason.
func RenderResult(w io.Writer, r Result) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "TABLE\tREQUESTED\tINSERTED\tREJECTED\tMISSING\tSTATUS")
	for _, t := range r.Tables {
		_, _ = fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%d\t%s\n", t.Table, t.Requested, t.Inserted, t.Rejected, t.Missing, t.Status)
	}
	_ = tw.Flush()
	_, _ = fmt.Fprintf(w, "\ninserted %d rows · missing %d\n", r.Inserted, r.Missing)
	for _, t := range r.Problems() {
		_, _ = fmt.Fprintf(w, "  %s (%s): %s\n", t.Table, t.Status, t.Error)
	}
	for _, s := range r.Sequences {
		_, _ = fmt.Fprintf(w, "advanced %s.%s sequence %d → %d\n", s.Table, s.Column, s.From, s.To)
	}
	if r.SequenceError != "" {
		_, _ = fmt.Fprintf(w, "warning: sequences not advanced, application inserts may reuse ids: %s\n", r.SequenceError)
	}
}
