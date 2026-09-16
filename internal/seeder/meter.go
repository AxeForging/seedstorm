package seeder

import (
	"fmt"
	"strconv"
	"time"
)

// meterWindow is how far back the rate looks. Short enough to follow a run
// that speeds up or slows down (a wide table after narrow ones), long enough
// not to jump with every batch.
const meterWindow = 15 * time.Second

// Meter turns row totals observed over time into a rate and an ETA. It is not
// safe for concurrent use; progress callbacks never overlap.
type Meter struct {
	start   time.Time
	samples []meterSample
}

type meterSample struct {
	at   time.Time
	rows int64
}

// Estimate is what a progress line shows.
type Estimate struct {
	Done, Total int64
	// Percent is 0–100.
	Percent float64
	// RowsPerSec is 0 until two observations are far enough apart to tell.
	RowsPerSec float64
	// ETA is how long the remaining rows take at RowsPerSec; 0 when unknown.
	ETA     time.Duration
	Elapsed time.Duration
}

// NewMeter starts measuring at start.
func NewMeter(start time.Time) *Meter {
	return &Meter{start: start, samples: []meterSample{{at: start, rows: 0}}}
}

// Observe records that done of total rows were written at now.
func (m *Meter) Observe(now time.Time, done, total int64) Estimate {
	m.samples = append(m.samples, meterSample{at: now, rows: done})
	// Keep one sample older than the window as the rate's baseline.
	cut := 0
	for cut+1 < len(m.samples) && now.Sub(m.samples[cut+1].at) > meterWindow {
		cut++
	}
	m.samples = m.samples[cut:]

	e := Estimate{Done: done, Total: max(total, done), Elapsed: now.Sub(m.start)}
	if e.Total > 0 {
		e.Percent = float64(done) / float64(e.Total) * 100
	}
	base := m.samples[0]
	if span := now.Sub(base.at); span >= 200*time.Millisecond && done > base.rows {
		e.RowsPerSec = float64(done-base.rows) / span.Seconds()
	}
	if e.RowsPerSec > 0 && e.Total > done {
		e.ETA = time.Duration(float64(e.Total-done) / e.RowsPerSec * float64(time.Second))
	}
	return e
}

// String renders "120.0k/1.5M rows (7.8%) · 4.2k rows/s · ETA 5m40s".
func (e Estimate) String() string {
	s := fmt.Sprintf("%s/%s rows (%.1f%%)", CompactCount(e.Done), CompactCount(e.Total), e.Percent)
	if e.RowsPerSec > 0 {
		s += " · " + CompactCount(int64(e.RowsPerSec)) + " rows/s"
	}
	if e.ETA > 0 {
		s += " · ETA " + ShortDuration(e.ETA)
	}
	return s
}

// CompactCount renders 950, 12.3k, 1.5M, 2.0B.
func CompactCount(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", float64(n)/1e9)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 10_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return strconv.FormatInt(n, 10)
}

// ShortDuration renders a duration to the second, hours included: 45s, 5m40s, 2h03m.
func ShortDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h, m, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%02dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm%02ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}
