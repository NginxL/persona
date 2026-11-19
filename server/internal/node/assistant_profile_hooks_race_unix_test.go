//go:build !windows

package node

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Two starts share one launcher profile. Hold the first start's source read
// open until the second has a chance to run: it must not start before the seed
// is complete, and a failed seed must remain retryable.
func TestAssistantProfileSeedSerializesConcurrentStarts(t *testing.T) {
	f := cliProfileTest(t)
	n := f.n
	n.home = os.Getenv("HOME")
	source := filepath.Join(n.home, ".claude")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	fifo := filepath.Join(source, "settings.json")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	settings := []byte(`{"hooks":{"PermissionRequest":[{"matcher":"Bash","hooks":[{"type":"command","command":"extension hook --persona-extension=agent-bypass-allow"}]}]}}`)
	release := make(chan struct{})
	writerDone := make(chan error, 1)
	go func() {
		file, err := os.OpenFile(fifo, os.O_WRONLY, 0)
		if err == nil {
			<-release
			_, err = file.Write(settings)
		}
		// Replace the FIFO before closing it: the first reader keeps its open file, and later startup reads see a regular file.
		regular := fifo + ".regular"
		if err == nil {
			err = os.WriteFile(regular, settings, 0o600)
		}
		if err == nil {
			err = os.Rename(regular, fifo)
		}
		if file != nil {
			if closeErr := file.Close(); err == nil {
				err = closeErr
			}
		}
		writerDone <- err
	}()

	type answer struct {
		p   *assistantProc
		err error
	}
	start := func(id string, ch chan<- answer) {
		p, err := n.spawnClaude(id, t.TempDir(), "claude", false, false, cliExec())
		ch <- answer{p: p, err: err}
	}
	profile := filepath.Join(n.dir, "profiles", "claude")
	first := make(chan answer, 1)
	go start("a5500000-0000-4000-8000-000000000561", first)
	waitFor(t, "first start to create the profile", func() bool {
		_, err := os.Stat(profile)
		return err == nil
	})
	second := make(chan answer, 1)
	go start("a5500000-0000-4000-8000-000000000562", second)
	returnedEarly := false
	select {
	case got := <-second:
		returnedEarly = true
		if got.p != nil {
			got.p.stop()
		}
		if got.err != nil {
			t.Fatalf("second start failed before the source was released: %v", got.err)
		}
	case <-time.After(400 * time.Millisecond):
	}
	close(release)
	wait := func(ch chan answer) answer {
		select {
		case got := <-ch:
			if got.err != nil {
				t.Fatal(got.err)
			}
			if got.p != nil {
				got.p.stop()
			}
			return got
		case <-time.After(10 * time.Second):
			t.Fatal("a start remained blocked after the source was released")
			return answer{}
		}
	}
	wait(first)
	if !returnedEarly {
		wait(second)
	}
	if err := <-writerDone; err != nil {
		t.Fatal(err)
	}
	if returnedEarly {
		t.Fatal("second start returned before the first start finished seeding the shared profile")
	}
	path := filepath.Join(profile, "settings.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
}
