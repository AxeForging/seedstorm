package web

import (
	"testing"
	"time"

	"github.com/AxeForging/seedstorm/internal/seeder"
)

type recordedControl struct {
	progress [][2]int
}

func (r *recordedControl) Write(p []byte) (int, error) { return len(p), nil }
func (r *recordedControl) Phase(string)                {}
func (r *recordedControl) Progress(done, total int, _ string) {
	r.progress = append(r.progress, [2]int{done, total})
}

// Enum coverage writes more rows than planned; the total then follows done,
// and "done == total" must not bypass throttling (108 events instead of 7 on
// the 36-table schema). The final tick still arrives, with the true total.
func TestRunProgress_ThrottlesPastThePlanAndAlwaysSendsTheFinalTick(t *testing.T) {
	defer func(d time.Duration) { progressEvery = d }(progressEvery)
	progressEvery = time.Hour

	jc := &recordedControl{}
	onProgress, _, finish := runProgress(jc, jobLogger(jc))
	for done := int64(100); done <= 5000; done += 100 {
		onProgress(seeder.Progress{Table: "t", RowsDone: done, RowsTotal: max(1000, done)})
	}
	finish()
	if len(jc.progress) > 3 {
		t.Fatalf("%d progress events for one throttle window: %v", len(jc.progress), jc.progress)
	}
	if last := jc.progress[len(jc.progress)-1]; last != [2]int{5000, 5000} {
		t.Fatalf("last event = %v, want the final 5000/5000", last)
	}

	// Nothing observed, nothing sent.
	empty := &recordedControl{}
	_, _, finishEmpty := runProgress(empty, jobLogger(empty))
	finishEmpty()
	if len(empty.progress) != 0 {
		t.Fatalf("finish without progress sent %v", empty.progress)
	}
}
