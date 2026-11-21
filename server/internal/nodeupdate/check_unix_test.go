//go:build unix

package nodeupdate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// scannedCheckFixture is a node-update-check that, like a file under endpoint protection that scans new executables,
// is slow (delay) only on its first run and answers at once after it. With "$0.hang" present it never answers.
func scannedCheckFixture(t *testing.T, dir, delay string) string {
	t.Helper()
	path := filepath.Join(dir, "persona-node")
	script := "#!/bin/sh\nif [ ! -e \"$0.scanned\" ]; then sleep " + delay + "; : >\"$0.scanned\"; fi\n" +
		"if [ -e \"$0.hang\" ]; then exec sleep 30; fi\n" +
		`printf '%s\n' '{"version":"next","os":"` + runtime.GOOS + `","arch":"` + runtime.GOARCH + `","program":"persona-node"}'` + "\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func withCheckLimits(t *testing.T, limits CheckLimits) {
	t.Helper()
	old := checkLimits
	checkLimits = limits
	t.Cleanup(func() { checkLimits = old })
}

// #12528: the first run of a new file waits for the scan; the same file's later runs keep the short limit.
func TestSelfCheckWaitsForFirstRunScan(t *testing.T) {
	withCheckLimits(t, CheckLimits{FirstRun: 5 * time.Second, Repeat: 200 * time.Millisecond})
	path := scannedCheckFixture(t, t.TempDir(), "0.5")
	check, err := runCheck(t.Context(), path)
	if err != nil || check.Version != "next" {
		t.Fatalf("slow first run: check=%+v err=%v", check, err)
	}
	if check, err := runCheck(t.Context(), path); err != nil || check.Version != "next" {
		t.Fatalf("repeat run: check=%+v err=%v", check, err)
	}
}

func TestSelfCheckRepeatKeepsShortLimit(t *testing.T) {
	withCheckLimits(t, CheckLimits{FirstRun: 5 * time.Second, Repeat: 200 * time.Millisecond})
	path := scannedCheckFixture(t, t.TempDir(), "0")
	if _, err := runCheck(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	// The file that passed now hangs: the repeat limit, not the first-run one, ends it.
	if err := os.WriteFile(path+".hang", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := runCheck(t.Context(), path)
	if err == nil || !strings.Contains(err.Error(), "node-update-check timed out after 200ms (this file passed before)") {
		t.Fatalf("repeat hang: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("repeat hang waited %s", elapsed)
	}
}

func TestSelfCheckUnresponsiveFirstRunTimesOut(t *testing.T) {
	withCheckLimits(t, CheckLimits{FirstRun: 300 * time.Millisecond, Repeat: 100 * time.Millisecond})
	path := filepath.Join(t.TempDir(), "persona-node")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexec sleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := runCheck(t.Context(), path)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) ||
		!strings.Contains(err.Error(), "node executable self-check: node-update-check timed out after 300ms (first run of this file)") {
		t.Fatalf("unresponsive first run: %v", err)
	}
	if strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("a timeout reads as a mismatch: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("unresponsive first run waited %s", elapsed)
	}
}

// A file replaced at the same path is a new file to the scanner: it gets the first-run limit again.
func TestSelfCheckReplacedFileGetsFirstRunLimit(t *testing.T) {
	withCheckLimits(t, CheckLimits{FirstRun: 5 * time.Second, Repeat: 200 * time.Millisecond})
	dir := t.TempDir()
	path := scannedCheckFixture(t, dir, "0")
	if _, err := runCheck(t.Context(), path); err != nil {
		t.Fatal(err)
	}
	next := filepath.Join(t.TempDir(), "persona-node")
	scannedCheckFixture(t, filepath.Dir(next), "0.5")
	if err := os.Rename(next, path); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path + ".scanned"); err != nil {
		t.Fatal(err)
	}
	if check, err := runCheck(t.Context(), path); err != nil || check.Version != "next" {
		t.Fatalf("replaced file: check=%+v err=%v", check, err)
	}
}
