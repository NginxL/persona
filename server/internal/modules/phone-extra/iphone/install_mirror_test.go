package iphone

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func serveInstallMirror(t *testing.T, status int, body []byte) (string, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &hits
}

func runMirrorRegressionInstall(t *testing.T, src InstallSource) (*Job, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("PERSONA_HOME", home)
	t.Setenv(BridgeEnv, "")
	t.Setenv("PATH", t.TempDir())
	m := New(filepath.Join(home, "iphone"))
	t.Cleanup(m.Close)
	m.logf = t.Logf
	if err := m.StartInstall(src); err != nil {
		t.Fatal(err)
	}
	return waitJob(t, m), filepath.Join(home, "bin", BridgeName)
}

func checkInstalledMirrorBinary(t *testing.T, path string, src InstallSource) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != src.BinarySHA256 {
		t.Fatal("the installed binary does not match binary_sha256")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("installed mode = %o, want 755", info.Mode().Perm())
	}
}

func TestInstallMirrorArchiveValidation(t *testing.T) {
	good, src := releaseBuild(t, "persona-iphone-bridge 9.9.9 (wire 1)")
	bad := append([]byte(nil), good...)
	bad[len(bad)-1] ^= 1 // same length, but not the archive named by the manifest
	short := good[:len(good)-1]

	for _, tc := range []struct {
		name           string
		firstStatus    int
		firstBody      []byte
		secondStatus   int
		secondBody     []byte
		wantState      string
		wantCode       string
		wantSecondHits int32
	}{
		{"same-length checksum mismatch then healthy", http.StatusOK, bad, http.StatusOK, good, JobDone, "", 1},
		{"all same-length checksum mismatches", http.StatusOK, bad, http.StatusOK, bad, JobFailed, CodeChecksumMismatch, 1},
		{"checksum mismatch then not found", http.StatusOK, bad, http.StatusNotFound, nil, JobFailed, CodeChecksumMismatch, 1},
		{"not found then healthy", http.StatusNotFound, nil, http.StatusOK, good, JobDone, "", 1},
		{"wrong size then healthy", http.StatusOK, short, http.StatusOK, good, JobDone, "", 1},
		{"all not found", http.StatusNotFound, nil, http.StatusNotFound, nil, JobFailed, CodeDownloadFailed, 1},
		{"all wrong sizes", http.StatusOK, short, http.StatusOK, short, JobFailed, CodeDownloadFailed, 1},
		{"healthy first", http.StatusOK, good, http.StatusOK, good, JobDone, "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.wantState == JobDone && runtime.GOOS == "windows" {
				t.Skip("the build is a shell script")
			}
			firstURL, firstHits := serveInstallMirror(t, tc.firstStatus, tc.firstBody)
			secondURL, secondHits := serveInstallMirror(t, tc.secondStatus, tc.secondBody)
			src.URLs = []string{firstURL, secondURL}
			job, installed := runMirrorRegressionInstall(t, src)
			if job.State != tc.wantState || job.Code != tc.wantCode || firstHits.Load() != 1 || secondHits.Load() != tc.wantSecondHits {
				t.Fatalf("job=%+v first_hits=%d second_hits=%d, want state=%s code=%s second_hits=%d",
					job, firstHits.Load(), secondHits.Load(), tc.wantState, tc.wantCode, tc.wantSecondHits)
			}
			if tc.wantState == JobDone {
				checkInstalledMirrorBinary(t, installed, src)
			} else if _, err := os.Stat(installed); !os.IsNotExist(err) {
				t.Fatalf("failed install left a bridge: %v", err)
			}
		})
	}
}

func TestInstallMirrorWrongBinaryDoesNotRefetch(t *testing.T) {
	good, src := releaseBuild(t, "persona-iphone-bridge 9.9.9 (wire 1)")
	src.BinarySHA256 = strings.Repeat("0", 64)
	firstURL, firstHits := serveInstallMirror(t, http.StatusOK, good)
	secondURL, secondHits := serveInstallMirror(t, http.StatusOK, good)
	src.URLs = []string{firstURL, secondURL}
	job, installed := runMirrorRegressionInstall(t, src)
	if job.State != JobFailed || job.Code != CodeChecksumMismatch || firstHits.Load() != 1 || secondHits.Load() != 0 {
		t.Fatalf("job=%+v first_hits=%d second_hits=%d", job, firstHits.Load(), secondHits.Load())
	}
	if _, err := os.Stat(installed); !os.IsNotExist(err) {
		t.Fatalf("failed install left a bridge: %v", err)
	}
}

func TestInstallMirrorCancellationStopsFallback(t *testing.T) {
	good, src := releaseBuild(t, "persona-iphone-bridge 9.9.9 (wire 1)")
	started := make(chan struct{})
	var firstHits atomic.Int32
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstHits.Add(1)
		close(started)
		<-r.Context().Done()
	}))
	t.Cleanup(first.Close)
	secondURL, secondHits := serveInstallMirror(t, http.StatusOK, good)

	home := t.TempDir()
	t.Setenv("PERSONA_HOME", home)
	t.Setenv(BridgeEnv, "")
	m := New(filepath.Join(home, "iphone"))
	t.Cleanup(m.Close)
	src.URLs = []string{first.URL, secondURL}
	if err := m.StartInstall(src); err != nil {
		t.Fatal(err)
	}
	defer m.Cancel()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the first mirror was not requested")
	}
	m.Cancel()
	job := waitJob(t, m)
	if job.State != JobCancelled || firstHits.Load() != 1 || secondHits.Load() != 0 {
		t.Fatalf("job=%+v first_hits=%d second_hits=%d", job, firstHits.Load(), secondHits.Load())
	}
}
