package iphone

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// releaseBuild is a bridge build as a release lists it: the gzip of a script answering version, and its hashes.
func releaseBuild(t *testing.T, says string) ([]byte, InstallSource) {
	t.Helper()
	body := []byte("#!/bin/sh\necho '" + says + "'\n")
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(body)
	zw.Close()
	outer, inner := sha256.Sum256(buf.Bytes()), sha256.Sum256(body)
	return buf.Bytes(), InstallSource{Size: int64(buf.Len()), SHA256: hex.EncodeToString(outer[:]), BinarySHA256: hex.EncodeToString(inner[:])}
}

// waitJob polls the job until it is no longer running, failing after 10 seconds.
func waitJob(t *testing.T, m *Manager) *Job {
	t.Helper()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	deadline := time.After(10 * time.Second)
	for m.Job().State == JobRunning {
		select {
		case <-tick.C:
		case <-deadline:
			t.Fatal("the job still runs")
		}
	}
	return m.Job()
}

// TestInstallPutsTheReleasesBridgeInPlace: the app's Install fetches the release's build (from the next address when
// one doesn't have it), checks it, and puts it where FindBridge looks; a wrong download or a binary that isn't the
// bridge leaves the one there was (#8199).
func TestInstallPutsTheReleasesBridgeInPlace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the build is a shell script")
	}
	home := t.TempDir()
	t.Setenv("PERSONA_HOME", home)
	t.Setenv(BridgeEnv, "")
	t.Setenv("PATH", t.TempDir())
	gz, src := releaseBuild(t, "persona-iphone-bridge 9.9.9 (wire 1)")
	wrong, wrongSrc := releaseBuild(t, "something else 1.0")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dl/bridge.gz":
			w.Write(gz)
		case "/dl/other.gz":
			w.Write(wrong)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	m := New(filepath.Join(home, "iphone"))
	t.Cleanup(m.Close)
	m.logf = t.Logf

	src.URLs = []string{srv.URL + "/dl/v1/bridge.gz", srv.URL + "/dl/bridge.gz"}
	if err := m.StartInstall(src); err != nil {
		t.Fatal(err)
	}
	if j := waitJob(t, m); j.State != JobDone || j.Kind != "install" || j.Stage != "" || j.Code != "" {
		t.Fatalf("job = %+v", j)
	}
	installed := filepath.Join(home, "bin", BridgeName)
	if got, err := FindBridge(); err != nil || got != installed {
		t.Fatalf("FindBridge = %q, %v", got, err)
	}
	if b := m.Bridge(t.Context()); !b.Installed || b.Version != "persona-iphone-bridge 9.9.9 (wire 1)" {
		t.Fatalf("Bridge = %+v", b)
	}
	before, _ := os.ReadFile(installed)

	for _, tc := range []struct {
		name string
		src  InstallSource
		code string
	}{
		{"bytes that aren't the release's", InstallSource{URLs: []string{srv.URL + "/dl/other.gz"}, Size: src.Size, SHA256: src.SHA256, BinarySHA256: src.BinarySHA256}, CodeDownloadFailed},
		{"a build that isn't the bridge", InstallSource{URLs: []string{srv.URL + "/dl/other.gz"}, Size: wrongSrc.Size, SHA256: wrongSrc.SHA256, BinarySHA256: wrongSrc.BinarySHA256}, CodeBadHelper},
		{"nowhere to fetch it", InstallSource{URLs: []string{srv.URL + "/missing.gz"}, Size: src.Size, SHA256: src.SHA256, BinarySHA256: src.BinarySHA256}, CodeDownloadFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := m.StartInstall(tc.src); err != nil {
				t.Fatal(err)
			}
			j := waitJob(t, m)
			if j.State != JobFailed || j.Code != tc.code {
				t.Fatalf("job = %+v, want %s", j, tc.code)
			}
			if after, _ := os.ReadFile(installed); !bytes.Equal(after, before) {
				t.Fatal("a failed install replaced the bridge there was")
			}
			if left, _ := filepath.Glob(filepath.Join(home, "bin", ".*")); len(left) != 0 {
				t.Fatalf("left over: %v", left)
			}
		})
	}
	if err := m.StartInstall(InstallSource{URLs: []string{"file:///etc/passwd"}, Size: 1, SHA256: src.SHA256, BinarySHA256: src.BinarySHA256}); Code(err) != "bad_request" {
		t.Fatalf("a file address: %v", err)
	}
	if err := m.StartInstall(InstallSource{}); Code(err) != "bad_request" {
		t.Fatalf("no build: %v", err)
	}
}

// TestDDIPresent: the developer disk image is there when its folder holds BuildManifest.plist (#8199).
func TestDDIPresent(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(DDIDirEnv, dir)
	if DDIPresent() {
		t.Fatal("an empty folder")
	}
	if err := os.WriteFile(filepath.Join(dir, "BuildManifest.plist"), []byte("<plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !DDIPresent() {
		t.Fatal("a folder with the manifest")
	}
}
