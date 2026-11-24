package node

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeCaffeinate stands in for /usr/bin/caffeinate: it writes its arguments down and holds for its -t.
func fakeCaffeinate(t *testing.T) (log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "calls")
	bin := filepath.Join(dir, "caffeinate")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho \"$@\" >> "+log+"\nexec sleep \"$3\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldPath, oldOS, oldAfter := caffeinatePath, installOS, awakeAfter
	caffeinatePath, installOS, awakeAfter = bin, "darwin", 4*time.Second
	t.Cleanup(func() { caffeinatePath, installOS, awakeAfter = oldPath, oldOS, oldAfter })
	return log
}

// An operation keeps the Mac awake for a while and no longer; the live screen as long as it is open, then not at all.
func TestTheMacStaysAwakeWhileOperatedOrWatched(t *testing.T) {
	log := fakeCaffeinate(t)
	var a awakeHold
	t.Cleanup(func() { a.mu.Lock(); a.opsUntil, a.watching = time.Time{}, 0; a.reconcile(); a.mu.Unlock() })

	a.operated()
	if h := a.held(); h <= 0 || h > awakeAfter {
		t.Fatalf("an operation: held %v", h)
	}
	calls := ""
	for deadline := time.Now().Add(3 * time.Second); calls == "" && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		calls = readFile(t, log)
	}
	if want := "-dimsu -t 4 -w " + strconv.Itoa(os.Getpid()); strings.TrimSpace(calls) != want {
		t.Fatalf("caffeinate %q, want %q", calls, want)
	}
	a.operated()
	if n := strings.Count(readFile(t, log), "\n"); n != 1 {
		t.Fatalf("another operation within the window renews nothing yet: %d caffeinates", n)
	}
	time.Sleep(awakeAfter + 500*time.Millisecond)
	if a.held() != 0 {
		t.Fatal("let go after the last operation's window")
	}

	done := a.watch()
	time.Sleep(awakeAfter + time.Second)
	if a.held() <= 0 {
		t.Fatal("watched past the window: still held (renewed)")
	}
	done()
	done() // a second close is nothing
	if a.held() != 0 {
		t.Fatal("the screen closed, nothing operated lately: let go at once")
	}
}

func TestNothingIsHeldOffAMac(t *testing.T) {
	log := fakeCaffeinate(t)
	installOS = "linux"
	var a awakeHold
	a.operated()
	a.watch()()
	if a.held() != 0 || readFile(t, log) != "" {
		t.Fatal("only a Mac")
	}
}

// The live screen's connection ends its watch when it closes, once.
func TestTheWatchedConnectionEndsTheWatch(t *testing.T) {
	fakeCaffeinate(t)
	server, client := net.Pipe()
	defer server.Close()
	ended := 0
	c := &watchedConn{Conn: client, done: func() { ended++ }}
	_ = c.Close()
	if ended != 1 {
		t.Fatal(ended)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, _ := os.ReadFile(p)
	return string(b)
}

// The real caffeinate on this Mac (PERSONA_AWAKE_E2E=1): an operation holds the display awake (pmset lists the
// assertion under its pid), and nothing of it is left once let go.
func TestAwakeOnThisMac(t *testing.T) {
	if os.Getenv("PERSONA_AWAKE_E2E") == "" {
		t.Skip("holds this Mac's display awake for a moment: PERSONA_AWAKE_E2E=1")
	}
	var a awakeHold
	a.operated()
	a.mu.Lock()
	pid := a.proc.Process.Pid
	a.mu.Unlock()
	time.Sleep(500 * time.Millisecond)
	out, _ := exec.Command("/usr/bin/pmset", "-g", "assertions").Output()
	if !regexp.MustCompile(fmt.Sprintf(`pid %d\(caffeinate\):[^\n]*PreventUserIdleDisplaySleep`, pid)).Match(out) {
		t.Fatalf("no display assertion from caffeinate %d:\n%s", pid, out)
	}
	a.mu.Lock()
	a.opsUntil = time.Time{}
	a.reconcile()
	a.mu.Unlock()
	time.Sleep(500 * time.Millisecond)
	if out, _ := exec.Command("/usr/bin/pmset", "-g", "assertions").Output(); strings.Contains(string(out), fmt.Sprintf("pid %d(caffeinate)", pid)) {
		t.Fatal("still held after letting go")
	}
	t.Logf("caffeinate %d held PreventUserIdleDisplaySleep, then let go", pid)
}

// "Lock": the operations' hold goes, then the display sleeps now.
func TestLockScreen(t *testing.T) {
	fakeCaffeinate(t)
	dir := t.TempDir()
	log := filepath.Join(dir, "pmset")
	bin := filepath.Join(dir, "pmset.sh")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\necho \"$@\" >> "+log+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := pmsetPath
	pmsetPath = bin
	t.Cleanup(func() { pmsetPath = old })
	awake.operated()
	if err := lockScreen(context.Background()); err != nil {
		t.Fatal(err)
	}
	if awake.held() != 0 || strings.TrimSpace(readFile(t, log)) != "displaysleepnow" {
		t.Fatalf("held %v, pmset %q", awake.held(), readFile(t, log))
	}
	installOS = "linux"
	if lockScreen(context.Background()) == nil {
		t.Fatal("only a Mac")
	}
}

// "No auto-lock while the extension is on": held for its window, renewed by each word from the hub, let go when off, by itself when the
// hub says nothing more.
func TestNoLockHoldsUntilLetGoOrRunOut(t *testing.T) {
	log := fakeCaffeinate(t)
	old := noLockFor
	noLockFor = 2 * time.Second
	t.Cleanup(func() { noLockFor = old; noLock.set(false) })
	noLock.set(true)
	if !noLock.held() {
		t.Fatal("held")
	}
	noLock.set(true)
	for deadline := time.Now().Add(3 * time.Second); strings.Count(readFile(t, log), "\n") < 2 && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
	}
	if calls := readFile(t, log); !strings.Contains(calls, "-dims -t 2 -w "+strconv.Itoa(os.Getpid())) || strings.Contains(calls, "-u") {
		t.Fatalf("caffeinate %q: -dims, no -u, its window, tied to node", calls)
	}
	noLock.set(false)
	if noLock.held() {
		t.Fatal("off: let go")
	}
	noLock.set(true)
	start := time.Now()
	// Not said again: it runs out by itself once caffeinate's window is over, whenever the loaded machine gets there.
	for noLock.held() {
		if time.Since(start) > noLockFor+10*time.Second {
			t.Fatal("not said again: run out by itself")
		}
		<-time.After(20 * time.Millisecond)
	}
	if took := time.Since(start); took < noLockFor/2 {
		t.Fatalf("let go after %v, before its %v window", took, noLockFor)
	}
}
