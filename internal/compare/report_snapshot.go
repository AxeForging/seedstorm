package compare

import "fmt"

// Report sides for SnapshotFromReport.
const (
	SideSource = "source"
	SideTarget = "target"
)

// SnapshotFromReport rebuilds one side's snapshot from a comparison, so a
// report already on screen can be exported without reading the database again.
// Target tables keep their own names (TargetTable when the match ignored case).
func SnapshotFromReport(r Report, side string) (Snapshot, error) {
	var info SnapshotInfo
	switch side {
	case SideSource:
		info = r.Source
	case SideTarget:
		info = r.Target
	default:
		return Snapshot{}, fmt.Errorf("unknown side %q (use source or target)", side)
	}
	snap := Snapshot{Label: info.Label, DBType: info.DBType, CountMode: info.CountMode, TakenAt: info.TakenAt, Tables: map[string]TableStat{}}
	for _, row := range r.Rows {
		stat, name := row.Source, row.Table
		if side == SideTarget {
			stat = row.Target
			if row.TargetTable != "" {
				name = row.TargetTable
			}
		}
		if stat != nil {
			snap.Tables[name] = *stat
		}
	}
	if len(snap.Tables) == 0 {
		return snap, fmt.Errorf("the %s side of this comparison has no tables", side)
	}
	// Shapes travel only when relationships were compared: exporting never scans.
	for _, d := range r.Relationships {
		shape := d.Source
		if side == SideTarget {
			shape = d.Target
		}
		if shape != nil {
			snap.Relationships = append(snap.Relationships, *shape)
		}
	}
	return snap, nil
}
