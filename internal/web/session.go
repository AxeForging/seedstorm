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

var sqlOpen = sql.Open

// ConnectionInfo is the non-secret view of an active connection, safe to
// surface in templates and logs.
type ConnectionInfo struct {
	Label  string `json:"label,omitempty"`
	DBType string `json:"dbType"`
	Host   string `json:"host"`
	Port   int    `json:"port"`
	DBName string `json:"dbName"`
	User   string `json:"user"`
	SSL    string `json:"ssl,omitempty"`
}

// Session holds a live database connection plus the cached schema introspected
// from it. The DSN (including the password) is intentionally not retained.
type Session struct {
	ID        string
	Info      ConnectionInfo
	DBType    string // driver name: "pgx" or "mysql"
	DSN       string // kept only so background jobs can re-open if needed
	conn      *sql.DB
	mu        sync.Mutex
	schema    *schema.Schema
	cachedAt  time.Time
	createdAt time.Time
}

// SessionRegistry holds active sessions keyed by their server-issued ID.
type SessionRegistry struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

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
	conn, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("open connection: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.PingContext(ctx); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	s := &Session{
		ID:        newSessionID(),
		Info:      info,
		DBType:    driver,
		DSN:       dsn,
		conn:      conn,
		createdAt: time.Now(),
	}
	r.mu.Lock()
	r.sessions[s.ID] = s
	r.mu.Unlock()
	return s, nil
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

// Schema returns the cached schema, introspecting if needed.
func (s *Session) Schema(force bool) (*schema.Schema, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !force && s.schema != nil {
		return s.schema, nil
	}
	tables, err := db.Introspect(s.DBType, s.DSN)
	if err != nil {
		return nil, err
	}
	out := &schema.Schema{Tables: make(map[string]schema.Table, len(tables))}
	for _, t := range tables {
		st := schema.Table{Columns: make(map[string]schema.Column, len(t.Columns))}
		for _, c := range t.Columns {
			sc := schema.Column{
				Type:      c.Type,
				PK:        c.IsPK,
				Nullable:  c.IsNullable,
				Generated: c.Generated != "",
				Faker:     faker.MapColumnToFaker(s.DBType, c),
			}
			if c.FK != nil {
				sc.FK = fmt.Sprintf("%s.%s", c.FK.TableName, c.FK.ColumnName)
			}
			st.Columns[c.Name] = sc
		}
		out.Tables[t.Name] = st
	}
	s.schema = out
	s.cachedAt = time.Now()
	return out, nil
}

// RawTables returns the raw introspected tables (with constraint metadata
// such as enum values, CHECK ranges, etc.) — re-introspects on each call.
func (s *Session) RawTables() ([]db.Table, error) {
	return db.Introspect(s.DBType, s.DSN)
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
