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

// EventKind discriminates the entries in a job's event stream.
type EventKind string

const (
	EventLog      EventKind = "log"
	EventPhase    EventKind = "phase"
	EventProgress EventKind = "progress"
)

// Event is a single entry on the job's event stream. The fields used depend
// on Kind: log carries Text; phase carries Text (the phase name); progress
// carries Done/Total and Text (label).
type Event struct {
	Seq   int       `json:"seq"`
	Time  time.Time `json:"time"`
	Kind  EventKind `json:"kind"`
	Text  string    `json:"text,omitempty"`
	Done  int       `json:"done,omitempty"`
	Total int       `json:"total,omitempty"`
}

// LogLine is the historical view of a log-only event, kept for callers that
// only care about the captured text.
type LogLine struct {
	Seq  int       `json:"seq"`
	Time time.Time `json:"time"`
	Text string    `json:"text"`
}

// JobControl is what runners receive: a writer for free-form log output plus
// hooks for emitting structured phase + progress events.
type JobControl interface {
	io.Writer
	Phase(name string)
	Progress(done, total int, label string)
}

// Job represents a unit of background work with a captured event stream.
type Job struct {
	ID        string
	Name      string
	Status    JobStatus
	StartedAt time.Time
	EndedAt   time.Time
	Err       error
	Result    map[string]any

	mu      sync.Mutex
	events  []Event
	subs    map[chan Event]struct{}
	cancel  context.CancelFunc
	closed  bool
	closeCh chan struct{}
}

// JobFunc is the body of a job.
type JobFunc func(ctx context.Context, jc JobControl) (map[string]any, error)

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
		subs:      make(map[chan Event]struct{}),
		cancel:    cancel,
		closeCh:   make(chan struct{}),
	}
	m.mu.Lock()
	m.jobs[job.ID] = job
	m.mu.Unlock()

	go func() {
		job.setStatus(JobRunning)
		ctrl := &jobWriter{job: job}
		result, err := fn(jctx, ctrl)
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
		subs := make([]chan Event, 0, len(job.subs))
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

// Subscribe returns a channel that receives newly emitted events plus a copy
// of all events emitted before subscription. The channel is closed when the
// job ends. Slow consumers drop events; the backlog is recoverable via Events().
func (j *Job) Subscribe() (<-chan Event, []Event) {
	j.mu.Lock()
	defer j.mu.Unlock()
	backlog := make([]Event, len(j.events))
	copy(backlog, j.events)
	if j.closed {
		ch := make(chan Event)
		close(ch)
		return ch, backlog
	}
	ch := make(chan Event, 64)
	j.subs[ch] = struct{}{}
	return ch, backlog
}

// Unsubscribe removes a previously registered subscriber.
func (j *Job) Unsubscribe(ch <-chan Event) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for c := range j.subs {
		if (<-chan Event)(c) == ch {
			delete(j.subs, c)
			return
		}
	}
}

// Events returns a snapshot of every event captured so far.
func (j *Job) Events() []Event {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]Event, len(j.events))
	copy(out, j.events)
	return out
}

// Lines returns a snapshot of just the log-kind events as LogLines.
func (j *Job) Lines() []LogLine {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]LogLine, 0, len(j.events))
	for _, e := range j.events {
		if e.Kind == EventLog {
			out = append(out, LogLine{Seq: e.Seq, Time: e.Time, Text: e.Text})
		}
	}
	return out
}

// Done returns a channel closed when the job completes.
func (j *Job) Done() <-chan struct{} { return j.closeCh }

func (j *Job) setStatus(s JobStatus) {
	j.mu.Lock()
	j.Status = s
	j.mu.Unlock()
}

func (j *Job) appendEvent(ev Event) {
	j.mu.Lock()
	ev.Seq = len(j.events) + 1
	ev.Time = time.Now()
	j.events = append(j.events, ev)
	subs := j.subs
	j.mu.Unlock()
	for ch := range subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

// jobWriter implements JobControl. Each Write call appends one or more log
// events split on newlines so structured zerolog output stays one-line-per-event.
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
			w.job.appendEvent(Event{Kind: EventLog, Text: line})
		}
	}
	return len(p), nil
}

func (w *jobWriter) Phase(name string) {
	w.job.appendEvent(Event{Kind: EventPhase, Text: name})
}

func (w *jobWriter) Progress(done, total int, label string) {
	w.job.appendEvent(Event{Kind: EventProgress, Text: label, Done: done, Total: total})
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
		return fmt.Sprintf("job-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
