//go:build integration

package integration_test

import (
	"bufio"
	"context"
	"database/sql"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AxeForging/seedstorm/internal/web"
)

// Helpers for web job tests: one in-process server shared by several browser
// clients (cookie jars), and an SSE reader that parses the job stream exactly
// as the workspace UI receives it.

// newWebJobsServer starts an in-process server and returns its base URL.
func newWebJobsServer(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	s, err := web.New(web.Options{
		Addr:            "127.0.0.1:0",
		ConnectionsPath: filepath.Join(dir, "connections.yaml"),
		ProfilesPath:    filepath.Join(dir, "profiles.yaml"),
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv.URL
}

// clientFor is a browser with its own cookie jar (its own active connection).
func clientFor(t *testing.T, base string) *webClient {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	return &webClient{t: t, base: base, http: &http.Client{
		Jar:           jar,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// connectEngine connects the client to dbName on the engine's server.
func (c *webClient) connectEngine(e engine, dbName string) {
	c.t.Helper()
	if e.driver == postgresDriver {
		c.connectPostgres(dbName)
		return
	}
	form := mysqlForm()
	form.Set("dbName", dbName)
	form.Set("label", dbName)
	res, err := c.http.PostForm(c.base+"/connect", form)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/" {
		c.t.Fatalf("connect %s = %d", dbName, res.StatusCode)
	}
}

// start POSTs a job endpoint and returns the job id.
func (c *webClient) start(path string, body any) string {
	c.t.Helper()
	var started jobSnapshot
	if code := c.json(http.MethodPost, path, body, &started); code != http.StatusAccepted || started.ID == "" {
		c.t.Fatalf("POST %s = %d (%+v)", path, code, started)
	}
	return started.ID
}

// streamEvent is one SSE event of a job stream.
type streamEvent struct {
	Kind  string // log | phase | progress | status | failure | end
	Seq   int
	Text  string // log line, phase name, or progress label
	Done  int
	Total int
	Phase string // phase the job was in when this event was emitted
	At    time.Time
}

// jobStream reads /api/jobs/{id}/stream in the background.
type jobStream struct {
	mu     sync.Mutex
	events []streamEvent
	status string
	errMsg string
	ended  bool
	readEr error
	notify chan struct{}
	closed chan struct{}
	cancel context.CancelFunc
}

var (
	seqData      = regexp.MustCompile(`^\[(\d+)\] (.*)$`)
	progressData = regexp.MustCompile(`^(\d+)/(\d+) ?(.*)$`)
)

// stream opens the job's SSE stream. The reader stops at the end event, on
// close(), or when the test ends.
func (c *webClient) stream(id string) *jobStream {
	c.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/jobs/"+id+"/stream", nil)
	res, err := c.http.Do(req)
	if err != nil {
		cancel()
		c.t.Fatalf("open stream: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		cancel()
		c.t.Fatalf("stream %s = %d", id, res.StatusCode)
	}
	s := &jobStream{notify: make(chan struct{}, 1), closed: make(chan struct{}), cancel: cancel}
	c.t.Cleanup(s.close)
	go s.read(res)
	return s
}

func (s *jobStream) close() {
	s.cancel()
	<-s.closed
}

func (s *jobStream) read(res *http.Response) {
	defer close(s.closed)
	defer res.Body.Close()
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	var event string
	var data []string
	phase := ""
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = append(data, strings.TrimPrefix(line, "data: "))
		case line == "":
			if event == "" {
				continue
			}
			ev := streamEvent{Kind: event, Text: strings.Join(data, "\n"), At: time.Now()}
			if m := seqData.FindStringSubmatch(ev.Text); m != nil && (event == "log" || event == "phase" || event == "progress") {
				ev.Seq, _ = strconv.Atoi(m[1])
				ev.Text = m[2]
			}
			if event == "progress" {
				if m := progressData.FindStringSubmatch(ev.Text); m != nil {
					ev.Done, _ = strconv.Atoi(m[1])
					ev.Total, _ = strconv.Atoi(m[2])
					ev.Text = m[3]
				}
			}
			if event == "phase" {
				phase = ev.Text
			}
			ev.Phase = phase
			s.mu.Lock()
			switch event {
			case "status":
				s.status = ev.Text
			case "failure":
				s.errMsg = ev.Text
			case "end":
				s.ended = true
			}
			s.events = append(s.events, ev)
			s.mu.Unlock()
			select {
			case s.notify <- struct{}{}:
			default:
			}
			event, data = "", nil
			if s.isEnded() {
				return
			}
		}
	}
	s.mu.Lock()
	s.readEr = sc.Err()
	s.mu.Unlock()
}

func (s *jobStream) isEnded() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ended
}

func (s *jobStream) snapshot() []streamEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]streamEvent(nil), s.events...)
}

// waitFor blocks until pred holds for some received event (returning it) or
// the timeout passes.
func (s *jobStream) waitFor(t *testing.T, timeout time.Duration, what string, pred func(streamEvent) bool) streamEvent {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		for _, ev := range s.snapshot() {
			if pred(ev) {
				return ev
			}
		}
		select {
		case <-s.notify:
		case <-s.closed:
			for _, ev := range s.snapshot() {
				if pred(ev) {
					return ev
				}
			}
			t.Fatalf("stream closed before %s (status %q, error %q)", what, s.status, s.errMsg)
		case <-deadline.C:
			t.Fatalf("no %s within %s", what, timeout)
		}
	}
}

// finish waits for the end event and returns every event plus the status.
func (s *jobStream) finish(t *testing.T, timeout time.Duration) ([]streamEvent, string, string) {
	t.Helper()
	select {
	case <-s.closed:
	case <-time.After(timeout):
		t.Fatalf("job stream did not end within %s", timeout)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.ended {
		t.Fatalf("stream closed without an end event: %v", s.readEr)
	}
	return append([]streamEvent(nil), s.events...), s.status, s.errMsg
}

func eventsOf(events []streamEvent, kind string) []streamEvent {
	var out []streamEvent
	for _, ev := range events {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

// assertContiguous fails when the stream skipped a sequence number: the UI
// would have lost that log line or progress tick.
func assertContiguous(t *testing.T, events []streamEvent) {
	t.Helper()
	want := 1
	for _, ev := range events {
		if ev.Seq == 0 {
			continue
		}
		if ev.Seq != want {
			t.Errorf("stream skipped events: got seq %d, want %d (%s %q)", ev.Seq, want, ev.Kind, ev.Text)
			return
		}
		want++
	}
}

var tableWrittenLine = regexp.MustCompile(`Table written\b.*`)

// tableWritten collects "Table written table=X rows=N" log lines by table.
func tableWritten(t *testing.T, events []streamEvent) map[string][]int {
	t.Helper()
	out := map[string][]int{}
	for _, ev := range eventsOf(events, "log") {
		if !tableWrittenLine.MatchString(ev.Text) {
			continue
		}
		table, rows := logField(ev.Text, "table"), logField(ev.Text, "rows")
		n, err := strconv.Atoi(rows)
		if table == "" || err != nil {
			t.Fatalf("malformed table log line: %q", ev.Text)
		}
		out[table] = append(out[table], n)
	}
	return out
}

func logField(line, key string) string {
	m := regexp.MustCompile(`\b` + regexp.QuoteMeta(key) + `=(\S+)`).FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	return m[1]
}

// assertProgressTruthful checks one phase's progress ticks: done never goes
// back, total never shrinks and never undercounts what was planned, and the
// last tick reports every row written (done == total == written).
func assertProgressTruthful(t *testing.T, events []streamEvent, phase string, planned, written int, skip func(streamEvent) bool) []streamEvent {
	t.Helper()
	var ticks []streamEvent
	for _, ev := range eventsOf(events, "progress") {
		if ev.Phase == phase && (skip == nil || !skip(ev)) {
			ticks = append(ticks, ev)
		}
	}
	if len(ticks) == 0 {
		t.Fatalf("no progress events in phase %q", phase)
	}
	t.Logf("%s: %d progress ticks, %d rows", phase, len(ticks), written)
	prev := streamEvent{}
	for i, ev := range ticks {
		if ev.Done < prev.Done {
			t.Errorf("tick %d: done went back %d -> %d (%q)", i, prev.Done, ev.Done, ev.Text)
		}
		if ev.Total < prev.Total {
			t.Errorf("tick %d: total shrank %d -> %d (%q)", i, prev.Total, ev.Total, ev.Text)
		}
		if ev.Done > ev.Total {
			t.Errorf("tick %d: done %d beyond total %d", i, ev.Done, ev.Total)
		}
		if ev.Total < planned {
			t.Errorf("tick %d: total %d below the %d rows planned", i, ev.Total, planned)
		}
		if ev.Total > written {
			t.Errorf("tick %d: total %d above the %d rows actually written", i, ev.Total, written)
		}
		prev = ev
	}
	last := ticks[len(ticks)-1]
	if last.Done != last.Total || last.Done != written {
		t.Errorf("last progress = %d/%d (%q), want %d/%d", last.Done, last.Total, last.Text, written, written)
	}
	return ticks
}

// activeQueries counts statements still running against dbName from other
// sessions (idle pooled connections do not count).
func activeQueries(t *testing.T, e engine, dbName string) (int, string) {
	t.Helper()
	admin := e.adminDB(t)
	defer admin.Close()
	query := `SELECT COUNT(*), COALESCE(string_agg(left(query, 80), ' | '), '') FROM pg_stat_activity
		WHERE datname = $1 AND pid <> pg_backend_pid() AND state IS NOT NULL AND state <> 'idle'`
	if e.driver == mysqlDriver {
		query = `SELECT COUNT(*), COALESCE(GROUP_CONCAT(LEFT(COALESCE(INFO, COMMAND), 80) SEPARATOR ' | '), '') FROM information_schema.PROCESSLIST
			WHERE DB = ? AND ID <> CONNECTION_ID() AND COMMAND NOT IN ('Sleep', 'Daemon')`
	}
	var n int
	var sample sql.NullString
	if err := admin.QueryRowContext(context.Background(), query, dbName).Scan(&n, &sample); err != nil {
		t.Fatalf("active queries: %v", err)
	}
	return n, sample.String
}

// waitNoActiveQueries polls until nothing runs against dbName.
func waitNoActiveQueries(t *testing.T, e engine, dbName string, bound time.Duration) {
	t.Helper()
	deadline := time.Now().Add(bound)
	for {
		n, sample := activeQueries(t, e, dbName)
		if n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d queries still running on %s %s after cancel: %s", n, e.name, dbName, sample)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func sumCounts(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

// wideScratch creates a scratch DB holding the n-table wide schema.
func wideScratch(t *testing.T, e engine, name string, n int) *sql.DB {
	t.Helper()
	_, conn := e.scratchDB(t, name)
	for _, stmt := range wideSchemaDDL(n, e.driver) {
		execSQL(t, conn, stmt)
	}
	return conn
}
