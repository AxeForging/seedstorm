package fsutil

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreateAtomic_CommitReplacesAndAbortKeepsTheOriginal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.yaml")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	aborted, err := CreateAtomic(path, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = aborted.WriteString("half written")
	aborted.Abort()
	if b, _ := os.ReadFile(path); string(b) != "old" {
		t.Fatalf("abort changed the file to %q", b)
	}

	f, err := CreateAtomic(path, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString("new")
	if b, _ := os.ReadFile(path); string(b) != "old" {
		t.Fatalf("file replaced before Commit: %q", b)
	}
	if err := f.Commit(); err != nil {
		t.Fatal(err)
	}
	f.Abort() // after Commit, a deferred Abort must not remove the result
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "new" || info.Mode().Perm() != 0o644 {
		t.Fatalf("after commit: %q mode %v", b, info.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
}
