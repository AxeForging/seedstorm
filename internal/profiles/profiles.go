// Package profiles persists named seed profiles — rule sets built in the web
// UI, TUI or by hand — so any surface can reuse them by name:
// `seedstorm seed --profile loadtest` runs exactly what the builder saved.
package profiles

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/goccy/go-yaml"

	"github.com/AxeForging/seedstorm/internal/fsutil"
	"github.com/AxeForging/seedstorm/internal/rules"
)

const fileVersion = 1

// Profile is a saved rule set.
type Profile struct {
	ID        string        `json:"id" yaml:"id"`
	CreatedAt time.Time     `json:"createdAt" yaml:"createdAt"`
	UpdatedAt time.Time     `json:"updatedAt" yaml:"updatedAt"`
	Rules     rules.RuleSet `json:"rules" yaml:"rules"`
}

// Name is the profile's display name, stored on its rule set.
func (p Profile) Name() string { return p.Rules.Name }

type storeFile struct {
	Version  int       `yaml:"version"`
	Profiles []Profile `yaml:"profiles"`
}

// Store reads and writes profiles in one YAML file.
type Store struct {
	path string
	mu   sync.Mutex
}

// ErrNotFound is returned when no profile matches.
var ErrNotFound = errors.New("profile not found")

// DefaultPath is $XDG_CONFIG_HOME/seedstorm/profiles.yaml (or ~/.config/...).
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve config dir: %w", err)
	}
	return filepath.Join(dir, "seedstorm", "profiles.yaml"), nil
}

// NewStore opens (without creating) the store at path.
func NewStore(path string) *Store { return &Store{path: path} }

// Path returns the backing file.
func (s *Store) Path() string { return s.path }

// List returns profiles sorted by name.
func (s *Store) List() ([]Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return nil, err
	}
	out := append([]Profile(nil), f.Profiles...)
	sort.SliceStable(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name()) < strings.ToLower(out[j].Name())
	})
	return out, nil
}

// Get finds a profile by id, or by name case-insensitively.
func (s *Store) Get(ref string) (Profile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return Profile{}, err
	}
	ref = strings.TrimSpace(ref)
	for _, p := range f.Profiles {
		if p.ID == ref {
			return p, nil
		}
	}
	for _, p := range f.Profiles {
		if strings.EqualFold(p.Name(), ref) {
			return p, nil
		}
	}
	return Profile{}, ErrNotFound
}

// Save creates (empty id) or replaces a profile. Names are required and must be
// unique case-insensitively; the rule set must be structurally valid.
func (s *Store) Save(id string, rs rules.RuleSet) (Profile, error) {
	rs.Name = strings.TrimSpace(rs.Name)
	if rs.Name == "" {
		return Profile{}, fmt.Errorf("profile name is required")
	}
	if rs.Version == 0 {
		rs.Version = rules.Version
	}
	if issues := rs.Validate(nil); rules.HasErrors(issues) {
		return Profile{}, fmt.Errorf("invalid profile: %s", issues[0])
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return Profile{}, err
	}
	if id != "" && !hasID(f.Profiles, id) {
		return Profile{}, ErrNotFound
	}
	for _, p := range f.Profiles {
		if p.ID != id && strings.EqualFold(p.Name(), rs.Name) {
			return Profile{}, fmt.Errorf("a profile named %q already exists", rs.Name)
		}
	}
	now := time.Now().UTC()
	if id != "" {
		for i, p := range f.Profiles {
			if p.ID == id {
				f.Profiles[i].Rules = rs
				f.Profiles[i].UpdatedAt = now
				return f.Profiles[i], s.write(f)
			}
		}
	}
	p := Profile{ID: newID(), CreatedAt: now, UpdatedAt: now, Rules: rs}
	f.Profiles = append(f.Profiles, p)
	return p, s.write(f)
}

// Delete removes a profile by id and reports whether it existed.
func (s *Store) Delete(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.read()
	if err != nil {
		return false, err
	}
	for i, p := range f.Profiles {
		if p.ID == id {
			f.Profiles = append(f.Profiles[:i], f.Profiles[i+1:]...)
			return true, s.write(f)
		}
	}
	return false, nil
}

// Resolve loads a profile reference used on the command line: an existing file
// path wins, otherwise a saved profile name or id. store may be nil.
func Resolve(ref string, store *Store) (*rules.RuleSet, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, nil
	}
	if info, err := os.Stat(ref); err == nil && !info.IsDir() {
		return rules.Load(ref)
	}
	if store == nil {
		return nil, fmt.Errorf("profile %q: no such file", ref)
	}
	p, err := store.Get(ref)
	if errors.Is(err, ErrNotFound) {
		names := []string{}
		if all, lerr := store.List(); lerr == nil {
			for _, q := range all {
				names = append(names, q.Name())
			}
		}
		hint := "no saved profiles yet"
		if len(names) > 0 {
			hint = "saved profiles: " + strings.Join(names, ", ")
		}
		return nil, fmt.Errorf("profile %q is neither a file nor a saved profile (%s)", ref, hint)
	}
	if err != nil {
		return nil, err
	}
	rs := p.Rules
	return &rs, nil
}

func (s *Store) read() (*storeFile, error) {
	b, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return &storeFile{Version: fileVersion}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read profiles: %w", err)
	}
	f := &storeFile{}
	if strings.TrimSpace(string(b)) == "" {
		f.Version = fileVersion
		return f, nil
	}
	if err := yaml.Unmarshal(b, f); err != nil {
		// Refuse to overwrite a file we cannot parse.
		return nil, fmt.Errorf("parse %s: %w", s.path, err)
	}
	return f, nil
}

func (s *Store) write(f *storeFile) error {
	f.Version = fileVersion
	if f.Profiles == nil {
		f.Profiles = []Profile{}
	}
	b, err := yaml.Marshal(f)
	if err != nil {
		return fmt.Errorf("encode profiles: %w", err)
	}
	return fsutil.WriteFileAtomic(s.path, b)
}

func hasID(list []Profile, id string) bool {
	for _, p := range list {
		if p.ID == id {
			return true
		}
	}
	return false
}

func newID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "p_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return "p_" + hex.EncodeToString(b)
}
