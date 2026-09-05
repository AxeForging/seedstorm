package web

import (
	"path/filepath"
	"testing"
)

// testOptions points every test server at a throwaway connection store so no
// test can read or write the developer's real ~/.config/seedstorm.
func testOptions(t *testing.T) Options {
	t.Helper()
	return Options{
		Addr:            "127.0.0.1:0",
		ConnectionsPath: filepath.Join(t.TempDir(), "connections.yaml"),
	}
}
