// Package fsutil holds small filesystem helpers shared by the on-disk stores.
package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
)

// Private permissions for files that may hold secrets or personal settings.
const (
	PrivateFileMode = 0o600
	PrivateDirMode  = 0o700
)

// WriteFileAtomic replaces path with data via a temp file and rename, so a
// crash never leaves a half-written file, and forces owner-only permissions
// even on a pre-existing file that was readable by others.
func WriteFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, PrivateDirMode); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()
	if err := tmp.Chmod(PrivateFileMode); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", filepath.Base(path), err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	if err := os.Chmod(path, PrivateFileMode); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	return nil
}

// AtomicFile is written in place of path and only replaces it on Commit, so a
// failed or interrupted write never leaves a truncated file behind.
type AtomicFile struct {
	*os.File
	path string
	mode os.FileMode
	done bool
}

// CreateAtomic starts writing path through a temporary file in the same
// directory, which gets mode on Commit.
func CreateAtomic(path string, mode os.FileMode) (*AtomicFile, error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	return &AtomicFile{File: tmp, path: path, mode: mode}, nil
}

// Commit closes the temporary file and moves it over path.
func (f *AtomicFile) Commit() error {
	if f.done {
		return nil
	}
	f.done = true
	tmpName := f.Name()
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close %s: %w", filepath.Base(f.path), err)
	}
	if err := os.Chmod(tmpName, f.mode); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("chmod %s: %w", filepath.Base(f.path), err)
	}
	if err := os.Rename(tmpName, f.path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("replace %s: %w", f.path, err)
	}
	return nil
}

// Abort discards the temporary file; after Commit it does nothing.
func (f *AtomicFile) Abort() {
	if f.done {
		return
	}
	f.done = true
	_ = f.Close()
	_ = os.Remove(f.Name())
}
