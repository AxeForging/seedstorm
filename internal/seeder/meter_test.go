package seeder

import (
	"testing"
	"time"
)

func TestMeter_RateAndETAFollowRecentThroughput(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	m := NewMeter(t0)

	first := m.Observe(t0.Add(100*time.Millisecond), 10, 1000)
	if first.RowsPerSec != 0 || first.ETA != 0 {
		t.Fatalf("rate after 100ms = %v eta %v, want unknown", first.RowsPerSec, first.ETA)
	}

	e := m.Observe(t0.Add(2*time.Second), 200, 1000)
	if e.RowsPerSec != 100 || e.ETA != 8*time.Second || e.Percent != 20 {
		t.Fatalf("estimate = %+v, want 100 rows/s, ETA 8s, 20%%", e)
	}

	// 30s later the run is ten times faster: the window forgets the slow start.
	for s := 3; s <= 32; s++ {
		e = m.Observe(t0.Add(time.Duration(s)*time.Second), 200+int64(s-2)*1000, 100_000)
	}
	if e.RowsPerSec < 950 || e.RowsPerSec > 1050 {
		t.Fatalf("rate = %.0f, want about 1000 once the slow start left the window", e.RowsPerSec)
	}
}

func TestMeter_TotalGrowsWhenRowsExceedTheRequest(t *testing.T) {
	m := NewMeter(time.Unix(0, 0))
	e := m.Observe(time.Unix(5, 0), 1200, 1000)
	if e.Total != 1200 || e.Percent != 100 || e.ETA != 0 {
		t.Fatalf("estimate = %+v, want total raised to done, 100%%, no ETA", e)
	}
	if got := m.Observe(time.Unix(6, 0), 1200, 0); got.Percent != 100 {
		t.Fatalf("zero requested total = %+v", got)
	}
}

func TestEstimate_String(t *testing.T) {
	cases := []struct {
		e    Estimate
		want string
	}{
		{Estimate{Done: 0, Total: 0}, "0/0 rows (0.0%)"},
		{Estimate{Done: 120_000, Total: 1_543_690, Percent: 7.77, RowsPerSec: 4200, ETA: 340 * time.Second}, "120.0k/1.5M rows (7.8%) · 4200 rows/s · ETA 5m40s"},
		{Estimate{Done: 2_000_000_000, Total: 1_000_000_000_000, Percent: 0.2, RowsPerSec: 55_000, ETA: 5 * time.Hour}, "2.0B/1000.0B rows (0.2%) · 55.0k rows/s · ETA 5h00m"},
	}
	for _, c := range cases {
		if got := c.e.String(); got != c.want {
			t.Errorf("String() = %q, want %q", got, c.want)
		}
	}
}

func TestShortDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0:                                "0s",
		1499 * time.Millisecond:          "1s",
		59 * time.Second:                 "59s",
		61 * time.Second:                 "1m01s",
		2*time.Hour + 3*time.Minute + 59: "2h03m",
	} {
		if got := ShortDuration(d); got != want {
			t.Errorf("ShortDuration(%v) = %q, want %q", d, got, want)
		}
	}
}
