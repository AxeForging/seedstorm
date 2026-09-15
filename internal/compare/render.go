package compare

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/AxeForging/seedstorm/internal/db"
)

// RenderReport writes the comparison as an aligned text table. onlyDiff hides
// tables whose counts and columns match.
func RenderReport(w io.Writer, r Report, onlyDiff bool) {
	_, _ = fmt.Fprintf(w, "Compare  source: %s (%s)  →  target: %s (%s)  · %s counts\n\n",
		r.Source.Label, engineName(r.Source.DBType), r.Target.Label, engineName(r.Target.DBType), r.Source.CountMode)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "TABLE\tSOURCE ROWS\tTARGET ROWS\tDELTA\tSOURCE SIZE\tTARGET SIZE\tSTATUS")
	shown := 0
	for _, row := range r.Rows {
		drift := len(row.MissingColumns) > 0 || len(row.ExtraColumns) > 0
		if onlyDiff && row.Status == StatusSame && !drift {
			continue
		}
		shown++
		status := string(row.Status)
		if drift {
			status += fmt.Sprintf(" (columns: -%d +%d)", len(row.MissingColumns), len(row.ExtraColumns))
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			row.Table, statRows(row.Source), statRows(row.Target), signed(row.Delta),
			statBytes(row.Source), statBytes(row.Target), status)
	}
	_ = tw.Flush()
	if onlyDiff && shown == 0 {
		_, _ = fmt.Fprintln(w, "  (every table matches)")
	}
	t := r.Totals
	_, _ = fmt.Fprintf(w, "\n%d tables · same %d · differs %d · source only %d · target only %d · column drift %d\n",
		len(r.Rows), t.Same, t.Differs, t.SourceOnly, t.TargetOnly, t.ColumnDrift)
	_, _ = fmt.Fprintf(w, "rows %d → %d · size %s → %s\n", t.SourceRows, t.TargetRows, FormatBytes(t.SourceBytes), FormatBytes(t.TargetBytes))
	if hasEstimates(r) {
		_, _ = fmt.Fprintln(w, "~ = estimated from database statistics; tables without a usable estimate were counted exactly")
	}
}

func hasEstimates(r Report) bool {
	for _, row := range r.Rows {
		if (row.Source != nil && row.Source.Estimated) || (row.Target != nil && row.Target.Estimated) {
			return true
		}
	}
	return false
}

// RenderPlan writes a mirror plan: what gets truncated, inserted, and skipped.
func RenderPlan(w io.Writer, p MirrorPlan) {
	_, _ = fmt.Fprintf(w, "Mirror plan · mode %s · scale %gx · %d rows into %d tables\n\n", p.Mode, p.Scale, p.TotalInsert, len(p.Entries))
	if len(p.Truncate) > 0 {
		_, _ = fmt.Fprintf(w, "TRUNCATE on target (%d tables): %s\n\n", len(p.Truncate), strings.Join(p.Truncate, ", "))
	}
	if len(p.Entries) == 0 {
		_, _ = fmt.Fprintln(w, "Nothing to insert: the target already matches.")
	} else {
		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(tw, "#\tTABLE\tSOURCE\tTARGET\tWANT\tINSERT\tWHY")
		for i, e := range p.Entries {
			_, _ = fmt.Fprintf(tw, "%d\t%s\t%d\t%d\t%d\t%d\t%s\n", i+1, e.Table, e.SourceRows, e.TargetRows, e.Want, e.Insert, e.Reason)
		}
		_ = tw.Flush()
	}
	if len(p.Skipped) > 0 {
		_, _ = fmt.Fprintf(w, "\nSkipped (%d):\n", len(p.Skipped))
		for _, s := range p.Skipped {
			if s.Detail != "" {
				_, _ = fmt.Fprintf(w, "  %s — %s (%s)\n", s.Table, s.Reason, s.Detail)
			} else {
				_, _ = fmt.Fprintf(w, "  %s — %s\n", s.Table, s.Reason)
			}
		}
	}
}

// FormatBytes renders a byte count for humans; unknown sizes render as "?".
func FormatBytes(n int64) string {
	if n < 0 {
		return "?"
	}
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func statRows(s *TableStat) string {
	if s == nil {
		return "—"
	}
	if s.Rows == db.UnknownCount {
		return "?"
	}
	if s.Estimated {
		return fmt.Sprintf("~%d", s.Rows)
	}
	return fmt.Sprintf("%d", s.Rows)
}

func statBytes(s *TableStat) string {
	if s == nil {
		return "—"
	}
	return FormatBytes(s.Bytes)
}

func signed(n int64) string {
	if n > 0 {
		return fmt.Sprintf("+%d", n)
	}
	return fmt.Sprintf("%d", n)
}

func engineName(driver string) string {
	if driver == "pgx" {
		return "postgres"
	}
	return driver
}
