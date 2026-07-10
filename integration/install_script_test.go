package integration_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallScript_InstallsVerifiedRelease(t *testing.T) {
	if runtime.GOOS == "windows" || (runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64") {
		t.Skip("unsupported installer platform")
	}
	tmp := t.TempDir()
	payload := filepath.Join(tmp, "payload")
	if err := os.Mkdir(payload, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(payload, "seedstorm"), []byte("#!/bin/sh\nprintf 'installed seedstorm\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	asset := "seedstorm-" + runtime.GOOS + "-" + runtime.GOARCH + ".tar.gz"
	archive := filepath.Join(tmp, asset)
	if out, err := exec.Command("tar", "-czf", archive, "-C", payload, "seedstorm").CombinedOutput(); err != nil {
		t.Fatalf("archive: %v %s", err, out)
	}
	b, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	sums := filepath.Join(tmp, "checksums.txt")
	if err := os.WriteFile(sums, []byte(fmt.Sprintf("%x  %s\n", sha256.Sum256(b), asset)), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(tmp, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	fake := `#!/bin/sh
set -eu
while [ "$#" -gt 0 ]; do case "$1" in -o) out="$2"; shift 2;; -*) shift;; *) url="$1"; shift;; esac; done
printf '%s\n' "$url" >> "$CURL_LOG"; case "$url" in */checksums.txt) cp "$SUMS" "$out";; *) cp "$ARCHIVE" "$out";; esac
`
	if err := os.WriteFile(filepath.Join(bin, "curl"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	dest, log := filepath.Join(tmp, "install"), filepath.Join(tmp, "curl.log")
	cmd := exec.Command("sh", filepath.Join("..", "install.sh"))
	cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "SEEDSTORM_VERSION=v0.11.1", "SEEDSTORM_INSTALL_DIR="+dest, "ARCHIVE="+archive, "SUMS="+sums, "CURL_LOG="+log)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("install: %v %s", err, out)
	}
	out, err := exec.Command(filepath.Join(dest, "seedstorm")).CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "installed seedstorm" {
		t.Fatalf("binary: %v %q", err, out)
	}
	logged, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logged), "/releases/download/v0.11.1/"+asset) {
		t.Fatalf("wrong URL: %s", logged)
	}
}
