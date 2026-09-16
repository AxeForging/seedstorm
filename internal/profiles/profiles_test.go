package profiles

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AxeForging/seedstorm/internal/rules"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(filepath.Join(t.TempDir(), "nested", "profiles.yaml"))
}

func loadtest() rules.RuleSet {
	return rules.RuleSet{
		Name:  "loadtest",
		Rules: []rules.Rule{{Column: "*email*", Action: rules.Action{Template: "lt+{{seq}}@{{domain}}"}}},
		Tables: map[string]rules.TableRules{
			"users": {Rows: 25, Columns: map[string]rules.Action{"status": {Value: "active"}}},
		},
	}
}

func TestStore_SaveSurvivesReopenWithPrivatePermissions(t *testing.T) {
	s := newStore(t)
	saved, err := s.Save("", loadtest())
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID == "" || saved.CreatedAt.IsZero() {
		t.Fatalf("saved = %+v", saved)
	}
	info, err := os.Stat(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("file mode = %o, want 600", perm)
	}

	reopened := NewStore(s.Path())
	got, err := reopened.Get("LOADTEST")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != saved.ID || got.Rules.Tables["users"].Rows != 25 || got.Rules.Rules[0].Template != "lt+{{seq}}@{{domain}}" {
		t.Fatalf("reopened = %+v", got)
	}
}

func TestStore_UpdateKeepsIDAndCreatedAt(t *testing.T) {
	s := newStore(t)
	first, _ := s.Save("", loadtest())
	edited := loadtest()
	edited.Description = "v2"
	second, err := s.Save(first.ID, edited)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || !second.CreatedAt.Equal(first.CreatedAt) || second.Rules.Description != "v2" {
		t.Fatalf("update = %+v", second)
	}
	all, _ := s.List()
	if len(all) != 1 {
		t.Fatalf("list = %d profiles, want 1", len(all))
	}
}

func TestStore_RejectsBadInput(t *testing.T) {
	s := newStore(t)
	if _, err := s.Save("", rules.RuleSet{}); err == nil || !strings.Contains(err.Error(), "name is required") {
		t.Errorf("empty name err = %v", err)
	}
	broken := loadtest()
	broken.Rules[0].Action = rules.Action{}
	if _, err := s.Save("", broken); err == nil || !strings.Contains(err.Error(), "invalid profile") {
		t.Errorf("invalid rules err = %v", err)
	}
	if _, err := s.Save("", loadtest()); err != nil {
		t.Fatal(err)
	}
	dupe := loadtest()
	dupe.Name = "LoadTest"
	if _, err := s.Save("", dupe); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("duplicate name err = %v", err)
	}
	if _, err := s.Save("p_missing", loadtest()); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id err = %v", err)
	}
}

func TestStore_DeleteAndListOrder(t *testing.T) {
	s := newStore(t)
	for _, name := range []string{"zeta", "Alpha", "mid"} {
		rs := loadtest()
		rs.Name = name
		if _, err := s.Save("", rs); err != nil {
			t.Fatal(err)
		}
	}
	all, _ := s.List()
	if all[0].Name() != "Alpha" || all[2].Name() != "zeta" {
		t.Fatalf("order = %s, %s, %s", all[0].Name(), all[1].Name(), all[2].Name())
	}
	removed, err := s.Delete(all[1].ID)
	if err != nil || !removed {
		t.Fatalf("delete = %v, %v", removed, err)
	}
	if removed, _ := s.Delete(all[1].ID); removed {
		t.Fatal("second delete reported removal")
	}
	if _, err := s.Get("mid"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get deleted = %v", err)
	}
}

func TestStore_CorruptFileIsReportedNotOverwritten(t *testing.T) {
	s := newStore(t)
	if err := os.MkdirAll(filepath.Dir(s.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Path(), []byte("profiles: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save("", loadtest()); err == nil {
		t.Fatal("save over a corrupt file succeeded")
	}
	b, _ := os.ReadFile(s.Path())
	if string(b) != "profiles: [\n" {
		t.Fatalf("corrupt file was modified: %q", b)
	}
}

func TestResolve_FileThenSavedNameWithHelpfulError(t *testing.T) {
	s := newStore(t)
	if _, err := s.Save("", loadtest()); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "rules.yaml")
	if err := os.WriteFile(file, []byte("name: from-file\nrules: [{column: note, setNull: true}]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fromFile, err := Resolve(file, s)
	if err != nil || fromFile.Name != "from-file" {
		t.Fatalf("file resolve = %+v, %v", fromFile, err)
	}
	byName, err := Resolve("loadtest", s)
	if err != nil || byName.Tables["users"].Rows != 25 {
		t.Fatalf("name resolve = %+v, %v", byName, err)
	}
	_, err = Resolve("nope", s)
	if err == nil || !strings.Contains(err.Error(), "saved profiles: loadtest") {
		t.Fatalf("missing resolve err = %v", err)
	}
	if rs, err := Resolve("", s); rs != nil || err != nil {
		t.Fatalf("empty ref = %v, %v", rs, err)
	}
}

func TestStore_IgnoreListSurvivesReopenAndBlankGlobIsRejected(t *testing.T) {
	s := newStore(t)
	withIgnore := loadtest()
	withIgnore.Ignore = []string{"flyway_*", "*_audit"}
	if _, err := s.Save("", withIgnore); err != nil {
		t.Fatal(err)
	}
	got, err := NewStore(s.Path()).Get("loadtest")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.Rules.Ignore, ",") != "flyway_*,*_audit" {
		t.Fatalf("reopened ignore = %v", got.Rules.Ignore)
	}

	blank := loadtest()
	blank.Name = "blank"
	blank.Ignore = []string{" "}
	if _, err := s.Save("", blank); err == nil || !strings.Contains(err.Error(), "ignore[0]") {
		t.Fatalf("blank ignore glob err = %v", err)
	}
}
