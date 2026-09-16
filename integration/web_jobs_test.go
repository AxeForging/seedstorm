//go:build integration

package integration_test

import (
	"database/sql"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// These tests drive workspace jobs (seed, gaps, mirror) through the in-process
// web server against real databases and read the job stream the way the UI
// does. Seeding writes on several connections at once, so progress, per-table
// logs, cancellation and concurrent jobs are checked against what the database
// actually holds afterwards.

type seedResult struct {
	Order       []string       `json:"order"`
	TotalRows   int            `json:"totalRows"`
	TableCounts map[string]int `json:"tableCounts"`
}

// runSeedJob starts a seed job, follows its stream to the end and returns the
// events and the finished job.
func runSeedJob(t *testing.T, c *webClient, body map[string]any, timeout time.Duration) ([]streamEvent, jobSnapshot) {
	t.Helper()
	id := c.start("/api/seed", body)
	events, status, errMsg := c.stream(id).finish(t, timeout)
	var job jobSnapshot
	c.json(http.MethodGet, "/api/jobs/"+id, nil, &job)
	if status != "done" || job.Status != "done" {
		t.Fatalf("seed job: stream status %q (%s), snapshot %q (%s)", status, errMsg, job.Status, job.Error)
	}
	return events, job
}

// assertSeedRunMatchesDatabase checks a finished seed run against the
// database, which was empty before it: result counts, one "Table written" line
// per table carrying the rows the table now holds, and truthful progress.
func assertSeedRunMatchesDatabase(t *testing.T, e engine, conn *sql.DB, events []streamEvent, job jobSnapshot, rows int) {
	t.Helper()
	assertContiguous(t, events)
	res := remarshal[seedResult](t, job.Result)
	db := tableCounts(t, e, conn)
	if len(res.Order) == 0 || len(res.Order) != len(db) {
		t.Fatalf("run scope has %d tables, database has %d", len(res.Order), len(db))
	}
	written := tableWritten(t, events)
	total := 0
	for _, table := range res.Order {
		n := db[table]
		total += n
		if n < rows {
			t.Errorf("%s: %d rows in the database, requested %d", table, n, rows)
		}
		if res.TableCounts[table] != n {
			t.Errorf("%s: result says %d rows, database holds %d", table, res.TableCounts[table], n)
		}
		if got := written[table]; len(got) != 1 || got[0] != n {
			t.Errorf("%s: Table written logs %v, want exactly one with rows=%d", table, got, n)
		}
		delete(written, table)
	}
	for table, logs := range written {
		t.Errorf("Table written for %s outside the run scope: %v", table, logs)
	}
	if res.TotalRows != total {
		t.Errorf("result totalRows = %d, database holds %d", res.TotalRows, total)
	}
	ticks := assertProgressTruthful(t, events, "insert", rows*len(res.Order), total, nil)
	for _, tick := range ticks {
		if table, _, _ := strings.Cut(tick.Text, " "); db[table] == 0 {
			t.Errorf("progress label %q does not name a seeded table", tick.Text)
			break
		}
	}
}

// A seed job's stream tells the truth: progress only moves forward and ends at
// every row written, each table is announced once with the rows it really got,
// and the result matches the database.
func TestWebJobs_SeedProgressIsTruthful(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			base := newWebJobsServer(t)
			t.Run("36-table schema with enums", func(t *testing.T) {
				_, conn := e.scratchDB(t, "ss_webjobs_seed")
				e.schema(t, conn)
				c := clientFor(t, base)
				c.connectEngine(e, "ss_webjobs_seed")
				const rows = 3000
				events, job := runSeedJob(t, c, map[string]any{"rows": rows, "workers": 4, "batchSize": 250}, 3*time.Minute)
				assertSeedRunMatchesDatabase(t, e, conn, events, job, rows)
			})
			t.Run("150-table wide schema", func(t *testing.T) {
				conn := wideScratch(t, e, "ss_webjobs_wide", 150)
				c := clientFor(t, base)
				c.connectEngine(e, "ss_webjobs_wide")
				const rows = 400
				events, job := runSeedJob(t, c, map[string]any{"rows": rows, "workers": 4, "batchSize": 100}, 3*time.Minute)
				assertSeedRunMatchesDatabase(t, e, conn, events, job, rows)
			})
		})
	}
}

// Cancelling a large seed stops it promptly: the job ends canceled, nothing is
// emitted after the end, no statement keeps running on the database, and the
// same connection seeds again right away.
func TestWebJobs_CancelIsPromptAndLeavesAUsableServer(t *testing.T) {
	const (
		tables       = 150
		rows         = 50_000
		cancelBound  = 15 * time.Second
		drainedBound = 10 * time.Second
	)
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			dbName := "ss_webjobs_cancel"
			conn := wideScratch(t, e, dbName, tables)
			c := clientFor(t, newWebJobsServer(t))
			c.connectEngine(e, dbName)

			id := c.start("/api/seed", map[string]any{"rows": rows, "workers": 4})
			live := c.stream(id)
			live.waitFor(t, time.Minute, "insert progress", func(ev streamEvent) bool {
				return ev.Kind == "progress" && ev.Phase == "insert"
			})

			canceledAt := time.Now()
			var snap jobSnapshot
			if code := c.json(http.MethodPost, "/api/jobs/"+id+"/cancel", nil, &snap); code != http.StatusOK {
				t.Fatalf("cancel = %d", code)
			}
			events, status, errMsg := live.finish(t, cancelBound)
			end := events[len(events)-1]
			if status != "canceled" {
				t.Fatalf("job ended %q (%s) after cancel, want canceled", status, errMsg)
			}
			t.Logf("canceled in %s", end.At.Sub(canceledAt).Round(time.Millisecond))
			c.json(http.MethodGet, "/api/jobs/"+id, nil, &snap)
			if snap.Status != "canceled" {
				t.Errorf("job snapshot status = %q, want canceled", snap.Status)
			}

			waitNoActiveQueries(t, e, dbName, drainedBound)

			// Nothing may be appended once the job ended: a fresh viewer replays
			// exactly the events the live viewer saw before its end event.
			replay, _, _ := c.stream(id).finish(t, 5*time.Second)
			lastSeq := func(evs []streamEvent) int {
				n := 0
				for _, ev := range evs {
					n = max(n, ev.Seq)
				}
				return n
			}
			if lastSeq(replay) != lastSeq(events) {
				for _, ev := range replay {
					if ev.Seq > lastSeq(events) {
						t.Errorf("event after the job ended: seq %d %s %q", ev.Seq, ev.Kind, ev.Text)
					}
				}
			}

			partial := tableCounts(t, e, conn)
			if written := sumCounts(partial); written >= tables*rows {
				t.Fatalf("cancel did not stop the run: %d rows written of %d", written, tables*rows)
			}

			// The same connection seeds again: every table gets exactly the new rows.
			followEvents, follow := runSeedJob(t, c, map[string]any{"rows": 5, "workers": 4}, 2*time.Minute)
			res := remarshal[seedResult](t, follow.Result)
			after := tableCounts(t, e, conn)
			if len(res.Order) != tables {
				t.Fatalf("follow-up seed covered %d tables, want %d", len(res.Order), tables)
			}
			for _, table := range res.Order {
				if delta := after[table] - partial[table]; delta != 5 || res.TableCounts[table] != 5 {
					t.Errorf("%s: follow-up added %d rows (result %d), want 5", table, delta, res.TableCounts[table])
				}
			}
			assertProgressTruthful(t, followEvents, "insert", 5*tables, 5*tables, nil)
		})
	}
}

// Gap fill and mirror stream progress the same way a seed does, and what they
// report matches what the database holds.
func TestWebJobs_GapsFillAndMirrorReportProgress(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			base := newWebJobsServer(t)

			t.Run("gaps fill", func(t *testing.T) {
				_, conn := e.scratchDB(t, "ss_webjobs_gaps")
				e.schema(t, conn)
				c := clientFor(t, base)
				c.connectEngine(e, "ss_webjobs_gaps")
				runSeedJob(t, c, map[string]any{"rows": 50, "tables": []string{"users"}, "workers": 4}, time.Minute)
				before := tableCounts(t, e, conn)

				const rows = 1500
				id := c.start("/api/gaps", map[string]any{"fill": true, "rows": rows, "workers": 4, "batchSize": 200})
				events, status, errMsg := c.stream(id).finish(t, 3*time.Minute)
				if status != "done" {
					t.Fatalf("gaps job = %q: %s", status, errMsg)
				}
				assertContiguous(t, events)
				var job jobSnapshot
				c.json(http.MethodGet, "/api/jobs/"+id, nil, &job)
				res := remarshal[struct {
					GapTables []string `json:"gapTables"`
					Filled    int      `json:"filled"`
				}](t, job.Result)
				after := tableCounts(t, e, conn)

				gap := map[string]bool{}
				for _, table := range res.GapTables {
					gap[table] = true
				}
				if len(gap) == 0 || gap["users"] || len(gap) == len(before) {
					t.Fatalf("gap tables = %v; want the empty tables only (populated before: %v)", res.GapTables, before)
				}
				written := tableWritten(t, events)
				added := 0
				for table, n := range after {
					switch {
					case gap[table]:
						if before[table] != 0 || n < rows {
							t.Errorf("gap table %s: %d -> %d rows, want empty -> at least %d", table, before[table], n, rows)
						}
						if got := written[table]; len(got) != 1 || got[0] != n {
							t.Errorf("%s: Table written logs %v, want exactly one with rows=%d", table, got, n)
						}
					case n != before[table]:
						t.Errorf("populated table %s changed: %d -> %d", table, before[table], n)
					}
					added += n - before[table]
				}
				if res.Filled != added {
					t.Errorf("result filled = %d, database gained %d rows", res.Filled, added)
				}
				assertProgressTruthful(t, events, "insert", rows*len(gap), added, nil)
			})

			t.Run("mirror", func(t *testing.T) {
				_, src := e.scratchDB(t, "ss_webjobs_msrc")
				_, tgt := e.scratchDB(t, "ss_webjobs_mtgt")
				e.schema(t, src)
				e.schema(t, tgt)
				c := clientFor(t, base)
				c.connectEngine(e, "ss_webjobs_mtgt")
				c.connectEngine(e, "ss_webjobs_msrc")
				runSeedJob(t, c, map[string]any{"rows": 800, "workers": 4}, 2*time.Minute)
				source := tableCounts(t, e, src)
				ids := c.sessionIDs()

				id := c.start("/api/mirror", map[string]any{
					"source": map[string]string{"id": ids["ss_webjobs_msrc"]}, "target": map[string]string{"id": ids["ss_webjobs_mtgt"]},
					"workers": 4, "batchSize": 200,
				})
				events, status, errMsg := c.stream(id).finish(t, 3*time.Minute)
				if status != "done" {
					t.Fatalf("mirror job = %q: %s", status, errMsg)
				}
				assertContiguous(t, events)
				var job jobSnapshot
				c.json(http.MethodGet, "/api/jobs/"+id, nil, &job)
				res := remarshal[struct {
					Plan struct {
						TotalInsert int `json:"totalInsert"`
					} `json:"plan"`
					Run struct {
						Inserted int `json:"inserted"`
						Missing  int `json:"missing"`
					} `json:"run"`
				}](t, job.Result)
				target := tableCounts(t, e, tgt)
				assertCounts(t, target, source, 1)
				if res.Run.Missing != 0 || res.Run.Inserted != sumCounts(target) {
					t.Errorf("mirror run inserted %d (missing %d), target holds %d", res.Run.Inserted, res.Run.Missing, sumCounts(target))
				}
				if res.Plan.TotalInsert != sumCounts(source) {
					t.Errorf("plan total %d, source holds %d", res.Plan.TotalInsert, sumCounts(source))
				}
				assertProgressTruthful(t, events, "seed", res.Plan.TotalInsert, sumCounts(target), func(ev streamEvent) bool {
					return strings.HasPrefix(ev.Text, "truncate ")
				})
			})
		})
	}
}

// Two seed jobs on two databases run at once on one server, each followed by
// viewers that come and go mid-run (a second tab, a page reload): both finish
// with counts that match their own database and their own stream.
func TestWebJobs_ConcurrentSeedJobs(t *testing.T) {
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			base := newWebJobsServer(t)
			names := []string{"ss_webjobs_conc_a", "ss_webjobs_conc_b"}
			const rows = 1500
			var wg sync.WaitGroup
			type outcome struct {
				events []streamEvent
				job    jobSnapshot
				status string
				errMsg string
			}
			outcomes := make([]outcome, len(names))
			conns := make([]*sql.DB, len(names))
			clients := make([]*webClient, len(names))
			ids := make([]string, len(names))
			for i, name := range names {
				conns[i] = wideScratch(t, e, name, 60)
				clients[i] = clientFor(t, base)
				clients[i].connectEngine(e, name)
			}
			for i := range names {
				ids[i] = clients[i].start("/api/seed", map[string]any{"rows": rows, "workers": 4, "batchSize": 100})
			}
			for i := range names {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					c := clients[i]
					main := c.stream(ids[i])
					// Viewers join, watch a few events and leave while the job writes.
				churn:
					for k := 0; k < 20; k++ {
						select {
						case <-main.closed:
							break churn
						default:
						}
						v := c.stream(ids[i])
						for seen := 0; seen < 2; seen++ {
							select {
							case <-v.notify:
							case <-v.closed:
								seen = 2
							}
						}
						v.close()
					}
					select {
					case <-main.closed:
					case <-time.After(3 * time.Minute):
						return
					}
					var job jobSnapshot
					c.json(http.MethodGet, "/api/jobs/"+ids[i], nil, &job)
					main.mu.Lock()
					outcomes[i] = outcome{events: main.events, job: job, status: main.status, errMsg: main.errMsg}
					main.mu.Unlock()
				}(i)
			}
			wg.Wait()
			for i, name := range names {
				o := outcomes[i]
				if o.status != "done" || o.job.Status != "done" {
					t.Fatalf("%s: stream %q (%s), snapshot %q (%s)", name, o.status, o.errMsg, o.job.Status, o.job.Error)
				}
				t.Run(name, func(t *testing.T) {
					assertSeedRunMatchesDatabase(t, e, conns[i], o.events, o.job, rows)
				})
			}
		})
	}
}
