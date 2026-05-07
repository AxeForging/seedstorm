package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"sync"
	"time"
)

// JobStatus represents the lifecycle state of a job.
type JobStatus string

const (
	JobPending  JobStatus = "pending"
	JobRunning  JobStatus = "running"
	JobDone     JobStatus = "done"
	JobFailed   JobStatus = "failed"
	JobCanceled JobStatus = "canceled"
)

// LogLine is a single structured log line emitted by a running job.
type LogLine struct {
	Seq  int       `json:"seq"`
	Time time.Time `json:"time"`
	Text string    `json:"text"`
}

// Job represents a unit of background work with a captured log stream.
type Job struct {
	ID        string
	Name      string
	Status    JobStatus
	StartedAt time.Time
	EndedAt   time.Time
	Err       error
	Result    map[string]any

	mu      sync.Mutex
	lines   []LogLine
	subs    map[chan LogLine]struct{}
	cancel  context.CancelFunc
	closed  bool
	closeCh chan struct{}
}

// JobFunc is the body of a job. The provided io.Writer streams log output.
// Returning a non-nil result map exposes structured data to the UI.
type JobFunc func(ctx context.Context, log io.Writer) (map[string]any, error)

// Manager owns the registry of active and recent jobs.
type Manager struct {
	mu   sync.RWMutex
	jobs map[string]*Job
}

// NewManager constructs an empty job manager.
func NewManager() *Manager {
	return &Manager{jobs: make(map[string]*Job)}
}

// Start registers a new job and runs fn in a goroutine.
func (m *Manager) Start(ctx context.Context, name string, fn JobFunc) *Job {
	jctx, cancel := context.WithCancel(ctx)
	job := &Job{
		ID:        newID(),
		Name:      name,
		Status:    JobPending,
		StartedAt: time.Now(),
		subs:      make(map[chan LogLine]struct{}),
		cancel:    cancel,
		closeCh:   make(chan struct{}),
	}
	m.mu.Lock()
	m.jobs[job.ID] = job
	m.mu.Unlock()

	go func() {
		job.setStatus(JobRunning)
		writer := &jobWriter{job: job}
		result, err := fn(jctx, writer)
		job.mu.Lock()
		job.EndedAt = time.Now()
		job.Result = result
		switch {
		case err != nil && jctx.Err() != nil:
			job.Status = JobCanceled
			job.Err = err
		case err != nil:
			job.Status = JobFailed
			job.Err = err
		default:
			job.Status = JobDone
		}
		// Snapshot subs to close after releasing the lock to avoid deadlocks
		// if a slow subscriber is also calling back into the job.
		subs := make([]chan LogLine, 0, len(job.subs))
		for ch := range job.subs {
			subs = append(subs, ch)
		}
		job.subs = nil
		job.closed = true
		close(job.closeCh)
		job.mu.Unlock()
		for _, ch := range subs {
			close(ch)
		}
	}()

	return job
}

// Get returns a job by ID.
func (m *Manager) Get(id string) (*Job, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	j, ok := m.jobs[id]
	return j, ok
}

// Cancel signals the job to stop. Safe to call on completed jobs.
func (m *Manager) Cancel(id string) bool {
	j, ok := m.Get(id)
	if !ok {
		return false
	}
	j.cancel()
	return true
}

// Subscribe returns a channel that receives newly emitted log lines plus all
// lines emitted before subscription. The channel is closed when the job ends.
// The caller must drain promptly; slow consumers drop lines.
func (j *Job) Subscribe() (<-chan LogLine, []LogLine) {
	j.mu.Lock()
	defer j.mu.Unlock()
	backlog := make([]LogLine, len(j.lines))
	copy(backlog, j.lines)
	if j.closed {
		ch := make(chan LogLine)
		close(ch)
		return ch, backlog
	}
	ch := make(chan LogLine, 64)
	j.subs[ch] = struct{}{}
	return ch, backlog
}

// Unsubscribe removes a previously registered subscriber.
func (j *Job) Unsubscribe(ch <-chan LogLine) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for c := range j.subs {
		if (<-chan LogLine)(c) == ch {
			delete(j.subs, c)
			return
		}
	}
}

// Lines returns a snapshot of all log lines captured so far.
func (j *Job) Lines() []LogLine {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]LogLine, len(j.lines))
	copy(out, j.lines)
	return out
}

// Done returns a channel closed when the job completes.
func (j *Job) Done() <-chan struct{} { return j.closeCh }

func (j *Job) setStatus(s JobStatus) {
	j.mu.Lock()
	j.Status = s
	j.mu.Unlock()
}

func (j *Job) appendLine(text string) {
	j.mu.Lock()
	line := LogLine{Seq: len(j.lines) + 1, Time: time.Now(), Text: text}
	j.lines = append(j.lines, line)
	subs := j.subs
	j.mu.Unlock()
	for ch := range subs {
		select {
		case ch <- line:
		default:
			// Drop on slow subscriber; the backlog is still recoverable via Lines().
		}
	}
}

// jobWriter implements io.Writer; each Write call appends a single line entry,
// splitting on newlines so structured zerolog output stays one-line-per-event.
type jobWriter struct {
	job *Job
	buf []byte
}

func (w *jobWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := indexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		line := string(w.buf[:i])
		w.buf = w.buf[i+1:]
		if line != "" {
			w.job.appendLine(line)
		}
	}
	return len(p), nil
}

func indexByte(b []byte, c byte) int {
	for i := 0; i < len(b); i++ {
		if b[i] == c {
			return i
		}
	}
	return -1
}

func newID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fallback to time-based id; effectively never hits in practice.
		return fmt.Sprintf("job-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
