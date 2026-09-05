package web

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/goccy/go-yaml"
)

const (
	storeVersion  = 1
	storeFileMode = 0o600
	storeDirMode  = 0o700
)

// SavedConnection is a connection the user asked seedstorm to remember. It
// lives on disk between runs, unlike a Session which exists only while the
// process does.
type SavedConnection struct {
	ID     string  `json:"id" yaml:"id"`
	Label  string  `json:"label" yaml:"label"`
	DBType string  `json:"dbType" yaml:"dbType"`
	Host   string  `json:"host,omitempty" yaml:"host,omitempty"`
	Port   int     `json:"port,omitempty" yaml:"port,omitempty"`
	DBName string  `json:"dbName,omitempty" yaml:"dbName,omitempty"`
	User   string  `json:"user,omitempty" yaml:"user,omitempty"`
	SSL    string  `json:"ssl,omitempty" yaml:"ssl,omitempty"`
	DSN    string  `json:"dsn,omitempty" yaml:"dsn,omitempty"`
	Params []Param `json:"params,omitempty" yaml:"params,omitempty"`

	// Password is written to disk only when the user opts in, and is never
	// serialised into an API response — HasPassword reports its presence
	// instead.
	Password    string `json:"-" yaml:"password,omitempty"`
	HasPassword bool   `json:"hasPassword" yaml:"-"`

	CreatedAt time.Time `json:"createdAt,omitempty" yaml:"createdAt,omitempty"`
	UsedAt    time.Time `json:"usedAt,omitempty" yaml:"usedAt,omitempty"`
}

type storeFile struct {
	Version     int               `yaml:"version"`
	Connections []SavedConnection `yaml:"connections"`
}

// ConnectionStore persists saved connections.
type ConnectionStore struct {
	path string
	mu   sync.Mutex
}

// DefaultConnectionsPath is where connections live when the caller does not
// override it: $XDG_CONFIG_HOME/seedstorm/connections.yaml, falling back to
// ~/.config/seedstorm/connections.yaml.
func DefaultConnectionsPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve config dir: %w", err)
	}
	return filepath.Join(dir, "seedstorm", "connections.yaml"), nil
}

// NewConnectionStore opens (but does not create) the store at path.
func NewConnectionStore(path string) *ConnectionStore {
	return &ConnectionStore{path: path}
}

// Path returns the file the store reads and writes.
func (s *ConnectionStore) Path() string { return s.path }

// DisplayPath renders the store location for the UI, shortening the user's home
// directory to ~ so the path does not dominate the page.
func (s *ConnectionStore) DisplayPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return s.path
	}
	if rel, err := filepath.Rel(home, s.path); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.Join("~", rel)
	}
	return s.path
}

// List returns every saved connection, most recently used first, with password
// material stripped.
func (s *ConnectionStore) List() ([]SavedConnection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return nil, err
	}
	out := make([]SavedConnection, 0, len(f.Connections))
	for _, c := range f.Connections {
		out = append(out, c.redacted())
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].UsedAt.After(out[j].UsedAt)
	})
	return out, nil
}

// Get returns one saved connection including its stored password, for the
// connect path. Callers must not hand the result to a template or API response
// without redacting it first.
func (s *ConnectionStore) Get(id string) (SavedConnection, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return SavedConnection{}, false, err
	}
	for _, c := range f.Connections {
		if c.ID == id {
			c.HasPassword = c.Password != ""
			return c, true, nil
		}
	}
	return SavedConnection{}, false, nil
}

// Save creates or updates a connection. An empty ID creates a new entry. On
// update, an empty Password keeps whatever was already stored, so the UI never
// has to round-trip the secret; use ClearPassword to drop it.
func (s *ConnectionStore) Save(c SavedConnection, clearPassword bool) (SavedConnection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return SavedConnection{}, err
	}
	c.Label = strings.TrimSpace(c.Label)
	c.Params = normalizeParams(c.Params)
	if c.Label == "" {
		c.Label = defaultLabel(c)
	}
	if clearPassword {
		c.Password = ""
	}
	if c.ID == "" {
		c.ID = newConnectionID()
		c.CreatedAt = time.Now().UTC()
		f.Connections = append(f.Connections, c)
	} else {
		found := false
		for i, existing := range f.Connections {
			if existing.ID != c.ID {
				continue
			}
			found = true
			c.CreatedAt = existing.CreatedAt
			if c.UsedAt.IsZero() {
				c.UsedAt = existing.UsedAt
			}
			if !clearPassword && c.Password == "" {
				c.Password = existing.Password
			}
			f.Connections[i] = c
			break
		}
		if !found {
			if c.CreatedAt.IsZero() {
				c.CreatedAt = time.Now().UTC()
			}
			f.Connections = append(f.Connections, c)
		}
	}
	if err := s.write(f); err != nil {
		return SavedConnection{}, err
	}
	return c.redacted(), nil
}

// Delete removes a connection. It reports whether anything was removed.
func (s *ConnectionStore) Delete(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return false, err
	}
	out := make([]SavedConnection, 0, len(f.Connections))
	removed := false
	for _, c := range f.Connections {
		if c.ID == id {
			removed = true
			continue
		}
		out = append(out, c)
	}
	if !removed {
		return false, nil
	}
	f.Connections = out
	return true, s.write(f)
}

// Touch records that a connection was just used, so the chooser can order by
// recency. A missing id is not an error — the connection may have been deleted
// in another tab.
func (s *ConnectionStore) Touch(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return err
	}
	for i, c := range f.Connections {
		if c.ID == id {
			f.Connections[i].UsedAt = time.Now().UTC()
			return s.write(f)
		}
	}
	return nil
}

// Import adds connections that are not already stored, matching on connection
// identity rather than id so importing the same browser presets twice cannot
// duplicate them. It returns how many were added.
func (s *ConnectionStore) Import(conns []SavedConnection) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return 0, err
	}
	seen := make(map[string]bool, len(f.Connections))
	labels := make(map[string]bool, len(f.Connections))
	for _, c := range f.Connections {
		seen[savedConnectionKey(c)] = true
		labels[strings.ToLower(c.Label)] = true
	}
	added := 0
	for _, c := range conns {
		c.Label = strings.TrimSpace(c.Label)
		c.Params = normalizeParams(c.Params)
		if c.Label == "" {
			c.Label = defaultLabel(c)
		}
		key := savedConnectionKey(c)
		if seen[key] || labels[strings.ToLower(c.Label)] {
			continue
		}
		c.ID = newConnectionID()
		if c.CreatedAt.IsZero() {
			c.CreatedAt = time.Now().UTC()
		}
		f.Connections = append(f.Connections, c)
		seen[key] = true
		labels[strings.ToLower(c.Label)] = true
		added++
	}
	if added == 0 {
		return 0, nil
	}
	return added, s.write(f)
}

// read loads the file. A missing file is an empty store, not an error; a
// corrupt one is an error so the server reports it instead of silently
// overwriting the user's connections.
func (s *ConnectionStore) read() (*storeFile, error) {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return &storeFile{Version: storeVersion}, nil
		}
		return nil, fmt.Errorf("read connections file: %w", err)
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return &storeFile{Version: storeVersion}, nil
	}
	var f storeFile
	if err := yaml.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.path, err)
	}
	if f.Version == 0 {
		f.Version = storeVersion
	}
	return &f, nil
}

// write persists the file atomically with owner-only permissions, tightening
// the mode of a pre-existing file that is readable by anyone else.
func (s *ConnectionStore) write(f *storeFile) error {
	f.Version = storeVersion
	if f.Connections == nil {
		f.Connections = []SavedConnection{}
	}
	b, err := yaml.Marshal(f)
	if err != nil {
		return fmt.Errorf("encode connections: %w", err)
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, storeDirMode); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".connections-*.yaml")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()
	if err := tmp.Chmod(storeFileMode); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if _, err := tmp.Write(b); err != nil {
		return fmt.Errorf("write connections: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close connections: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("replace connections file: %w", err)
	}
	// The rename keeps the temp file's mode, but an operator may have loosened
	// the directory; make the intent explicit either way.
	if err := os.Chmod(s.path, storeFileMode); err != nil {
		return fmt.Errorf("chmod connections file: %w", err)
	}
	return nil
}

func (c SavedConnection) redacted() SavedConnection {
	c.HasPassword = c.Password != ""
	c.Password = ""
	return c
}

// Info converts a saved connection into the non-secret view used by sessions.
func (c SavedConnection) Info() ConnectionInfo {
	return ConnectionInfo{
		Label:  c.Label,
		DBType: c.DBType,
		Host:   c.Host,
		Port:   c.Port,
		DBName: c.DBName,
		User:   c.User,
		SSL:    c.SSL,
		Params: c.Params,
	}
}

// Target renders the connection the way it is shown in the chooser.
func (c SavedConnection) Target() string {
	if c.Host == "" && c.DSN != "" {
		return "connection string"
	}
	host := c.Host
	if host == "" {
		host = "localhost"
	}
	target := host
	if c.Port > 0 {
		target += ":" + strconv.Itoa(c.Port)
	}
	if c.DBName != "" {
		target += "/" + c.DBName
	}
	if c.User != "" {
		return c.User + "@" + target
	}
	return target
}

func defaultLabel(c SavedConnection) string {
	if c.DBName != "" {
		return c.DBName
	}
	if c.Host != "" {
		return c.Host
	}
	return normalizeDriver(c.DBType) + " connection"
}

// savedConnectionKey identifies a connection by what it points at, so the same
// database saved twice under different labels is recognised as one.
func savedConnectionKey(c SavedConnection) string {
	driver := normalizeDriver(c.DBType)
	if c.DSN != "" && c.Host == "" && c.DBName == "" {
		return "dsn:" + driver + ":" + c.DSN
	}
	return strings.Join([]string{
		driver,
		strings.ToLower(c.Host),
		strconv.Itoa(c.Port),
		c.DBName,
		c.User,
	}, "|")
}

func newConnectionID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "c" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return "c_" + hex.EncodeToString(b)
}
