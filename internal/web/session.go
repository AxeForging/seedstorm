package web

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/faker"
	"github.com/AxeForging/seedstorm/internal/schema"
)

const sessionCookieName = "seedstorm_session"

// connectTimeout bounds both opening a session and testing a candidate
// connection, so a "test" that passes means a connect will too.
const connectTimeout = 5 * time.Second

var sqlOpen = sql.Open

// ConnectionInfo is the non-secret view of an active connection, safe to
// surface in templates and logs.
type ConnectionInfo struct {
	Label  string  `json:"label,omitempty"`
	DBType string  `json:"dbType"`
	Host   string  `json:"host"`
	Port   int     `json:"port"`
	DBName string  `json:"dbName"`
	User   string  `json:"user"`
	SSL    string  `json:"ssl,omitempty"`
	Params []Param `json:"params,omitempty"`
}

// Session holds a live database connection plus the cached schema introspected
// from it. The DSN (including the password) is intentionally not retained.
type Session struct {
	// SavedID is the saved connection this session was opened from, if any.
	SavedID   string
	ID        string
	Info      ConnectionInfo
	DBType    string // driver name: "pgx" or "mysql"
	DSN       string // kept only so background jobs can re-open if needed
	conn      *sql.DB
	mu        sync.Mutex
	schema    *schema.Schema
	cachedAt  time.Time
	createdAt time.Time
	// loading is the introspection in flight: callers wait for it instead of
	// starting another, and the session lock is never held across it.
	loading *schemaLoad

	// Row counts of the workspace, cached until a run writes or the user
	// refreshes; countsLoading is the count in flight.
	counts        map[string]int64
	countsAt      time.Time
	countsLoading *countsLoad

	accessMu sync.Mutex
	access   *accessView

	// Relationship shapes measured on this connection (see shapes.go).
	shapes shapeCache
}

// SessionRegistry holds active sessions keyed by their server-issued ID.
type SessionRegistry struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

// sessionConnMaxIdle is how long a session's pooled database connection may
// sit unused before it is closed; the next query opens a new one.
var sessionConnMaxIdle = 2 * time.Minute

// NewSessionRegistry constructs an empty registry.
func NewSessionRegistry() *SessionRegistry {
	return &SessionRegistry{sessions: make(map[string]*Session)}
}

// Open dials the database and registers a new session. The caller is
// responsible for setting the session cookie on the HTTP response.
func (r *SessionRegistry) Open(info ConnectionInfo, password string) (*Session, error) {
	driver, dsn, err := buildDSN(info, password)
	if err != nil {
		return nil, err
	}
	return r.open(driver, dsn, info)
}

// OpenDSN dials an already-built DSN and registers a new session.
func (r *SessionRegistry) OpenDSN(driver, dsn string, info ConnectionInfo) (*Session, error) {
	return r.open(driver, dsn, info)
}

func (r *SessionRegistry) open(driver, dsn string, info ConnectionInfo) (*Session, error) {
	if existing := r.findByDSN(driver, dsn); existing != nil {
		return existing, nil
	}
	conn, err := sqlOpen(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("open connection: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()
	if err := conn.PingContext(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	// Sessions live until disconnect; idle ones should not pin connections.
	conn.SetConnMaxIdleTime(sessionConnMaxIdle)
	s := &Session{
		ID:        newSessionID(),
		Info:      info,
		DBType:    driver,
		DSN:       dsn,
		conn:      conn,
		createdAt: time.Now(),
	}
	r.add(s)
	return s, nil
}

func (r *SessionRegistry) add(s *Session) {
	r.mu.Lock()
	r.sessions[s.ID] = s
	r.mu.Unlock()
}

func (r *SessionRegistry) findByDSN(driver, dsn string) *Session {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, s := range r.sessions {
		if s.DBType == driver && s.DSN == dsn {
			return s
		}
	}
	return nil
}

// Get fetches a session by ID.
func (r *SessionRegistry) Get(id string) (*Session, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.sessions[id]
	return s, ok
}

// All returns every active session, ordered by createdAt (oldest first).
func (r *SessionRegistry) All() []*Session {
	r.mu.RLock()
	out := make([]*Session, 0, len(r.sessions))
	for _, s := range r.sessions {
		out = append(out, s)
	}
	r.mu.RUnlock()
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].createdAt.Before(out[i].createdAt) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func sessionConnectionKey(s *Session) string {
	if s == nil {
		return ""
	}
	info := s.Info
	return strings.Join([]string{
		strings.ToLower(info.DBType),
		strings.ToLower(info.Host),
		strconv.Itoa(info.Port),
		info.DBName,
		info.User,
	}, "|")
}

func dedupeConnections(conns []*Session, activeID string) []*Session {
	seen := make(map[string]int, len(conns))
	out := make([]*Session, 0, len(conns))
	for _, sess := range conns {
		key := sessionConnectionKey(sess)
		if idx, ok := seen[key]; ok {
			if sess.ID == activeID && out[idx].ID != activeID {
				out[idx] = sess
			}
			continue
		}
		seen[key] = len(out)
		out = append(out, sess)
	}
	return out
}

// Pick returns any session that is not the given one (used to fall back to a
// remaining session after disconnecting the active one).
func (r *SessionRegistry) Pick(exclude string) *Session {
	for _, s := range r.All() {
		if s.ID != exclude {
			return s
		}
	}
	return nil
}

// Close drops a session, closing its DB connection.
func (r *SessionRegistry) Close(id string) {
	r.mu.Lock()
	s := r.sessions[id]
	delete(r.sessions, id)
	r.mu.Unlock()
	if s != nil && s.conn != nil {
		_ = s.conn.Close()
	}
}

// Conn returns the live *sql.DB.
func (s *Session) Conn() *sql.DB { return s.conn }

// OpenRunConn opens a short-lived database handle for mutating background jobs.
// Long-lived Serve sessions can outlive DDL resets that recreate enum/user
// types; a fresh handle avoids stale driver-side type metadata during inserts.
func (s *Session) OpenRunConn(ctx context.Context) (*sql.DB, error) {
	conn, err := sqlOpen(s.DBType, s.DSN)
	if err != nil {
		return nil, fmt.Errorf("open run connection: %w", err)
	}
	if err := conn.PingContext(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ping run connection: %w", err)
	}
	return conn, nil
}

// schemaLoadTimeout bounds one introspection of a session's database.
var schemaLoadTimeout = 5 * time.Minute

type schemaLoad struct {
	done chan struct{}
	sc   *schema.Schema
	err  error
}

// Schema returns the cached schema, introspecting if needed. Concurrent calls
// share one introspection, run on the session's own connection, without
// holding the session lock while the database answers.
func (s *Session) Schema(force bool) (*schema.Schema, error) {
	s.mu.Lock()
	if !force && s.schema != nil {
		sc := s.schema
		s.mu.Unlock()
		return sc, nil
	}
	if l := s.loading; l != nil {
		s.mu.Unlock()
		<-l.done
		return l.sc, l.err
	}
	l := &schemaLoad{done: make(chan struct{})}
	s.loading = l
	s.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), schemaLoadTimeout)
	tables, err := db.IntrospectConn(ctx, s.conn, s.DBType, nil)
	cancel()
	if err == nil {
		l.sc = faker.BuildSchema(s.DBType, tables)
	}
	l.err = err

	s.mu.Lock()
	s.loading = nil
	if err == nil {
		s.schema = l.sc
		s.cachedAt = time.Now()
	}
	s.mu.Unlock()
	close(l.done)
	return l.sc, l.err
}

// RawTables introspects the session's database again (constraint metadata such
// as enum values and CHECK ranges), on its own connection.
func (s *Session) RawTables() ([]db.Table, error) {
	ctx, cancel := context.WithTimeout(context.Background(), schemaLoadTimeout)
	defer cancel()
	return db.IntrospectConn(ctx, s.conn, s.DBType, nil)
}

// SetSchema overrides the cached schema (used by upload/paste flows).
func (s *Session) SetSchema(sc *schema.Schema) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.schema = sc
	s.cachedAt = time.Now()
}

// fromRequest extracts the session referenced by the request's cookie.
func (r *SessionRegistry) fromRequest(req *http.Request) (*Session, error) {
	c, err := req.Cookie(sessionCookieName)
	if err != nil {
		return nil, errors.New("not connected")
	}
	s, ok := r.Get(c.Value)
	if !ok {
		return nil, errors.New("not connected")
	}
	return s, nil
}

func setSessionCookie(w http.ResponseWriter, id string) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    id,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   -1,
	})
}

func newSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("sess-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

type countsLoad struct {
	done   chan struct{}
	counts map[string]int64
	at     time.Time
}

// countConcurrency is how many tables a workspace counts at once.
const countConcurrency = 2

// Counts returns the tables' row counts: the cached ones unless force is set
// or they were invalidated. Concurrent calls share one count. A table whose
// count failed is missing from the map (unknown, never 0).
func (s *Session) Counts(ctx context.Context, tables []string, force bool) (map[string]int64, time.Time) {
	s.mu.Lock()
	if !force && s.counts != nil {
		counts, at := s.counts, s.countsAt
		s.mu.Unlock()
		return counts, at
	}
	if l := s.countsLoading; l != nil {
		s.mu.Unlock()
		<-l.done
		return l.counts, l.at
	}
	l := &countsLoad{done: make(chan struct{})}
	s.countsLoading = l
	s.mu.Unlock()

	lim := db.DefaultCountLimits
	lim.Concurrency = countConcurrency
	counts, _ := db.CountTablesWithin(ctx, s.conn, s.DBType, tables, lim, nil)
	l.counts, l.at = counts, time.Now().UTC()

	s.mu.Lock()
	s.countsLoading = nil
	if ctx.Err() == nil {
		s.counts, s.countsAt = l.counts, l.at
	}
	s.mu.Unlock()
	close(l.done)
	return l.counts, l.at
}

// CachedCounts returns the cached counts without counting (nil when none).
func (s *Session) CachedCounts() (map[string]int64, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counts, s.countsAt
}

// InvalidateCounts drops the cached counts: a run wrote to the database.
func (s *Session) InvalidateCounts() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counts, s.countsAt = nil, time.Time{}
	s.shapes.reset()
}
