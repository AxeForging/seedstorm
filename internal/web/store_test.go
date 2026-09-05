package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *ConnectionStore {
	t.Helper()
	return NewConnectionStore(filepath.Join(t.TempDir(), "seedstorm", "connections.yaml"))
}

func TestConnectionStore_missingFileIsEmpty(t *testing.T) {
	store := NewConnectionStore(filepath.Join(t.TempDir(), "nope", "connections.yaml"))
	conns, err := store.List()
	if err != nil {
		t.Fatalf("List on a missing file should succeed: %v", err)
	}
	if len(conns) != 0 {
		t.Fatalf("conns = %+v, want empty", conns)
	}
}

func TestConnectionStore_roundTripsEveryField(t *testing.T) {
	store := newTestStore(t)
	saved, err := store.Save(SavedConnection{
		Label:  "staging",
		DBType: "mysql",
		Host:   "db.internal",
		Port:   3307,
		DBName: "app",
		User:   "seedstorm",
		SSL:    "require",
		Params: []Param{
			{Name: "allowCleartextPasswords", Value: "1"},
			{Name: "tls", Value: "skip-verify"},
		},
		Password: "s3cret",
	}, false)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if saved.ID == "" {
		t.Fatal("Save should assign an id")
	}

	// A second store over the same path proves the data survives a restart.
	reopened := NewConnectionStore(store.Path())
	got, ok, err := reopened.Get(saved.ID)
	if err != nil || !ok {
		t.Fatalf("Get after reopen: ok=%v err=%v", ok, err)
	}
	if got.Label != "staging" || got.DBType != "mysql" || got.Host != "db.internal" ||
		got.Port != 3307 || got.DBName != "app" || got.User != "seedstorm" || got.SSL != "require" {
		t.Fatalf("fields did not survive: %+v", got)
	}
	if len(got.Params) != 2 || got.Params[0].Name != "allowCleartextPasswords" || got.Params[1].Value != "skip-verify" {
		t.Fatalf("params did not survive: %+v", got.Params)
	}
	if got.Password != "s3cret" {
		t.Fatalf("password did not survive for the connect path: %q", got.Password)
	}
}

func TestConnectionStore_passwordIsOptIn(t *testing.T) {
	store := newTestStore(t)
	saved, err := store.Save(SavedConnection{
		Label: "no-secret", DBType: "postgres", Host: "h", Port: 5432,
		DBName: "app", User: "u", Password: "hunter2",
	}, true) // clearPassword: the user did not opt in
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if saved.HasPassword {
		t.Fatal("HasPassword should be false when the password was not stored")
	}
	raw, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	if strings.Contains(string(raw), "hunter2") {
		t.Fatalf("password reached disk without opt-in:\n%s", raw)
	}
}

func TestConnectionStore_listRedactsPasswords(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Save(SavedConnection{
		Label: "with-secret", DBType: "postgres", Host: "h", Port: 5432,
		DBName: "app", User: "u", Password: "hunter2",
	}, false); err != nil {
		t.Fatalf("Save: %v", err)
	}
	conns, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(conns) != 1 {
		t.Fatalf("conns = %+v", conns)
	}
	if conns[0].Password != "" {
		t.Fatalf("List leaked the password: %q", conns[0].Password)
	}
	if !conns[0].HasPassword {
		t.Fatal("HasPassword should report that a password is stored")
	}
}

func TestConnectionStore_updateKeepsPasswordUnlessCleared(t *testing.T) {
	store := newTestStore(t)
	saved, err := store.Save(SavedConnection{
		Label: "keeps", DBType: "postgres", Host: "h", Port: 5432,
		DBName: "app", User: "u", Password: "hunter2",
	}, false)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Edit without resending the secret.
	updated := saved
	updated.Password = ""
	updated.DBName = "app2"
	if _, err := store.Save(updated, false); err != nil {
		t.Fatalf("update: %v", err)
	}
	got, _, _ := store.Get(saved.ID)
	if got.Password != "hunter2" {
		t.Fatalf("password should survive an edit that omits it, got %q", got.Password)
	}
	if got.DBName != "app2" {
		t.Fatalf("edit did not apply: %+v", got)
	}

	// Explicitly dropping it.
	if _, err := store.Save(updated, true); err != nil {
		t.Fatalf("clear: %v", err)
	}
	got, _, _ = store.Get(saved.ID)
	if got.Password != "" {
		t.Fatalf("password should be gone, got %q", got.Password)
	}
}

func TestConnectionStore_permissions(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.Save(SavedConnection{Label: "p", DBType: "postgres", DBName: "app", User: "u"}, false); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fi, err := os.Stat(store.Path())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Fatalf("file mode = %o, want 600", mode)
	}
	di, err := os.Stat(filepath.Dir(store.Path()))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if mode := di.Mode().Perm(); mode != 0o700 {
		t.Fatalf("dir mode = %o, want 700", mode)
	}
}

func TestConnectionStore_tightensLoosePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "connections.yaml")
	if err := os.WriteFile(path, []byte("version: 1\nconnections: []\n"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	store := NewConnectionStore(path)
	if _, err := store.Save(SavedConnection{Label: "p", DBType: "postgres", DBName: "app", User: "u"}, false); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := fi.Mode().Perm(); mode != 0o600 {
		t.Fatalf("mode = %o, want the store to tighten it to 600", mode)
	}
}

func TestConnectionStore_delete(t *testing.T) {
	store := newTestStore(t)
	saved, _ := store.Save(SavedConnection{Label: "gone", DBType: "postgres", DBName: "app", User: "u"}, false)
	removed, err := store.Delete(saved.ID)
	if err != nil || !removed {
		t.Fatalf("Delete: removed=%v err=%v", removed, err)
	}
	if _, ok, _ := store.Get(saved.ID); ok {
		t.Fatal("connection still present after delete")
	}
	removed, err = store.Delete("c_missing")
	if err != nil {
		t.Fatalf("deleting an unknown id should not error: %v", err)
	}
	if removed {
		t.Fatal("deleting an unknown id should report nothing removed")
	}
}

func TestConnectionStore_importIsIdempotent(t *testing.T) {
	store := newTestStore(t)
	presets := []SavedConnection{
		{Label: "local pg", DBType: "postgres", Host: "localhost", Port: 5432, DBName: "app", User: "u"},
		{Label: "local mysql", DBType: "mysql", Host: "localhost", Port: 3306, DBName: "app", User: "u"},
	}
	added, err := store.Import(presets)
	if err != nil || added != 2 {
		t.Fatalf("first import: added=%d err=%v", added, err)
	}
	added, err = store.Import(presets)
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if added != 0 {
		t.Fatalf("re-importing the same presets added %d entries", added)
	}
	conns, _ := store.List()
	if len(conns) != 2 {
		t.Fatalf("store holds %d connections, want 2", len(conns))
	}

	// The same database under a different label is still the same connection.
	added, _ = store.Import([]SavedConnection{
		{Label: "renamed", DBType: "postgres", Host: "localhost", Port: 5432, DBName: "app", User: "u"},
	})
	if added != 0 {
		t.Fatal("importing the same target under a new label should be a no-op")
	}
}

func TestConnectionStore_touchOrdersByRecency(t *testing.T) {
	store := newTestStore(t)
	first, _ := store.Save(SavedConnection{Label: "first", DBType: "postgres", DBName: "a", User: "u"}, false)
	second, _ := store.Save(SavedConnection{Label: "second", DBType: "postgres", DBName: "b", User: "u"}, false)
	if err := store.Touch(second.ID); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := store.Touch(first.ID); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	conns, _ := store.List()
	if conns[0].Label != "first" {
		t.Fatalf("most recently used should sort first, got %q", conns[0].Label)
	}
	if err := store.Touch("c_missing"); err != nil {
		t.Fatalf("touching an unknown id should be a no-op, got %v", err)
	}
}

func TestConnectionStore_corruptFileIsReportedNotOverwritten(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "connections.yaml")
	body := "connections: [this is not valid yaml\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	store := NewConnectionStore(path)
	if _, err := store.List(); err == nil {
		t.Fatal("a corrupt store should surface an error")
	}
	raw, _ := os.ReadFile(path)
	if string(raw) != body {
		t.Fatalf("corrupt file was rewritten:\n%s", raw)
	}
}

func TestConnectionStore_defaultLabelAndTarget(t *testing.T) {
	store := newTestStore(t)
	saved, err := store.Save(SavedConnection{DBType: "postgres", Host: "h", Port: 5432, DBName: "orders", User: "u"}, false)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if saved.Label != "orders" {
		t.Fatalf("unlabelled connection should fall back to the database name, got %q", saved.Label)
	}
	if got := saved.Target(); got != "u@h:5432/orders" {
		t.Fatalf("Target() = %q", got)
	}
	dsnOnly := SavedConnection{DBType: "mysql", DSN: "u:p@tcp(h:3306)/db"}
	if got := dsnOnly.Target(); got != "connection string" {
		t.Fatalf("Target() for a raw DSN = %q", got)
	}
}

func TestSavedConnection_Info(t *testing.T) {
	c := SavedConnection{
		Label: "l", DBType: "mysql", Host: "h", Port: 3306, DBName: "d", User: "u", SSL: "disable",
		Params:   []Param{{Name: "tls", Value: "true"}},
		Password: "should not travel",
	}
	info := c.Info()
	if info.Label != "l" || info.DBType != "mysql" || info.Port != 3306 || info.SSL != "disable" {
		t.Fatalf("info = %+v", info)
	}
	if len(info.Params) != 1 || info.Params[0].Name != "tls" {
		t.Fatalf("params lost: %+v", info.Params)
	}
}
