//go:build !windows

package voice

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// holdingOutput and detached are the background part of a key_command: `sleep 30` sharing the command's output (an
// agent it starts, `cmd & echo token`), or with its output sent elsewhere.
const (
	holdingOutput = "sleep 30 &"
	detached      = "sleep 30 >/dev/null 2>&1 &"
)

// backgroundKeyCommand is a key_command that starts background, writes its pid to the answered file and then runs
// the shell code in then. The background process is killed when the test ends either way.
func backgroundKeyCommand(t *testing.T, background, then string) (*Provider, string) {
	t.Helper()
	pidFile := filepath.Join(t.TempDir(), "pid")
	t.Cleanup(func() {
		if pid, err := readBackgroundPid(pidFile); err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return &Provider{ID: "rt", KeyCommand: background + " echo $! > '" + pidFile + "'; " + then}, pidFile
}

func readBackgroundPid(pidFile string) (int, error) {
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err == nil && pid <= 0 {
		err = fmt.Errorf("pid %d", pid)
	}
	return pid, err
}

// backgroundRunning: pid has not exited. A killed orphan is a zombie until it is reaped, and stays one where nobody
// reaps (a container without an init), so a zombie counts as gone.
func backgroundRunning(pid int) bool {
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return false
	}
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
		if i := bytes.LastIndexByte(b, ')'); i >= 0 && i+2 < len(b) && b[i+2] == 'Z' {
			return false
		}
	}
	return true
}

// waitBackgroundGone fails the test when the background process whose pid is in pidFile is still running 5 s after
// credential returned.
func waitBackgroundGone(t *testing.T, pidFile string) {
	t.Helper()
	pid, err := readBackgroundPid(pidFile)
	if err != nil {
		t.Fatalf("the key_command never started its background process: %v", err)
	}
	waitFor(t, fmt.Sprintf("background process %d stopped after credential returned", pid), func() bool { return !backgroundRunning(pid) })
}

// credentialWithin runs p.credential and fails the test if it has not returned within 10 s.
func credentialWithin(t *testing.T, p *Provider) (string, error) {
	t.Helper()
	type result struct {
		key string
		err error
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		key, err := p.credential(context.Background())
		done <- result{key, err}
	}()
	select {
	case r := <-done:
		if took := time.Since(start); took > 10*time.Second {
			t.Fatalf("credential took %v: it waited for the background process", took)
		}
		return r.key, r.err
	case <-time.After(10 * time.Second):
		t.Fatal("credential still waits for the background process after 10s")
		return "", nil
	}
}

// A key_command that leaves a background process holding its output (an agent it starts, `cmd & echo token`) yields
// its credential once the command itself has exited: the call start is not held until that process ends.
func TestKeyCommandIsNotHeldByABackgroundProcessKeepingItsOutput(t *testing.T) {
	p, _ := backgroundKeyCommand(t, holdingOutput, "echo k")
	key, err := credentialWithin(t, p)
	if err != nil || key != "k" {
		t.Fatalf("credential = %q, %v; want \"k\", nil", key, err)
	}
}

// What a key_command leaves running does not outlive the call, whether it holds the command's output or not. The
// command runs in a group of its own for every conversation and every models or voices list, so a leftover would be
// one more process per call that neither the call nor stopping the extension ever stops.
func TestKeyCommandLeavesNothingRunning(t *testing.T) {
	for name, background := range map[string]string{"holding its output": holdingOutput, "detached": detached} {
		t.Run(name, func(t *testing.T) {
			p, pidFile := backgroundKeyCommand(t, background, "echo k")
			if key, err := credentialWithin(t, p); err != nil || key != "k" {
				t.Fatalf("credential = %q, %v; want \"k\", nil", key, err)
			}
			waitBackgroundGone(t, pidFile)
		})
	}
}

// A key_command that fails, or prints nothing, is still an error when a background process holds its output, and
// that process is stopped too.
func TestKeyCommandFailureWithABackgroundProcessIsAnError(t *testing.T) {
	for then, want := range map[string]string{
		"echo k; exit 3": "rt: key_command: exit status 3",
		"true":           "rt: key_command printed nothing",
	} {
		p, pidFile := backgroundKeyCommand(t, holdingOutput, then)
		if _, err := credentialWithin(t, p); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: credential error = %v; want %q", then, err, want)
		}
		waitBackgroundGone(t, pidFile)
	}
}
