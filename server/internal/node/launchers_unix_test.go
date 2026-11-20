//go:build !windows

package node

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// A ~/.zshrc that leaves something running in the background, holding the shell's output open (#10952): the launcher
// list comes back once the shell is done, not when that child is (the hub gives up on the computer after 30 s, and
// every ask after it waited behind this one), and it is cached for the next ask within the minute.
func TestLaunchersDoNotWaitForWhatTheRcFileLeavesRunning(t *testing.T) {
	if _, err := exec.LookPath("zsh"); err != nil {
		t.Skip("no zsh")
	}
	home := t.TempDir()
	pidFile := filepath.Join(home, "pid")
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("sleep 300 &\necho $! > "+pidFile+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", home)
	t.Setenv("SHELL", "/bin/zsh")
	killPidFileAtCleanup(t, pidFile)

	launcherCache.Lock()
	at, list := launcherCache.at, launcherCache.list
	codex, settings, scripts := launcherCache.codex, launcherCache.settings, launcherCache.scripts
	launcherCache.at, launcherCache.list = time.Time{}, nil
	launcherCache.Unlock()
	t.Cleanup(func() {
		launcherCache.Lock()
		launcherCache.at, launcherCache.list = at, list
		launcherCache.codex, launcherCache.settings, launcherCache.scripts = codex, settings, scripts
		launcherCache.Unlock()
	})

	type answer struct {
		list []proto.Launcher
		err  error
	}
	ask := func() (answer, time.Duration) {
		t.Helper()
		began := time.Now()
		done := make(chan answer, 1)
		go func() {
			ls, err := (&Node{}).launchers(context.Background())
			done <- answer{ls, err}
		}()
		select {
		case a := <-done:
			return a, time.Since(began)
		case <-time.After(15 * time.Second):
			t.Fatal("the launcher list still waits for the rc file's background child after 15 s")
			return answer{}, 0
		}
	}
	a, took := ask()
	t.Logf("first ask: %d launchers in %v (error %v)", len(a.list), took, a.err)
	if a.err != nil || len(a.list) == 0 {
		t.Fatalf("launchers %v, error %v; want the list (nothing at all: plain claude)", a.list, a.err)
	}
	if took > 10*time.Second {
		t.Errorf("first ask took %v, want within ~3 s of the shell finishing", took)
	}
	if again, took := ask(); took > time.Second || len(again.list) != len(a.list) {
		t.Errorf("second ask: %d launchers in %v, want the cached %d at once", len(again.list), took, len(a.list))
	}
}

func TestLaunchersRefreshReplacesTheCachedList(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("no zsh")
	}
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(zsh, filepath.Join(bin, "zsh")); err != nil {
		t.Fatal(err)
	}
	rc := filepath.Join(home, ".zshrc")
	if err := os.WriteFile(rc, []byte("alias custom-a='claude'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", home)
	t.Setenv("SHELL", zsh)
	t.Setenv("PATH", bin)

	launcherCache.Lock()
	at, list := launcherCache.at, launcherCache.list
	codex, settings, scripts := launcherCache.codex, launcherCache.settings, launcherCache.scripts
	launcherCache.at, launcherCache.list = time.Time{}, nil
	launcherCache.Unlock()
	t.Cleanup(func() {
		launcherCache.Lock()
		launcherCache.at, launcherCache.list = at, list
		launcherCache.codex, launcherCache.settings, launcherCache.scripts = codex, settings, scripts
		launcherCache.Unlock()
	})
	n := &Node{}
	first, err := n.launchers(context.Background())
	if err != nil || len(first) != 1 || first[0].Name != "custom-a" {
		t.Fatalf("initial discovery: %v, %v", first, err)
	}
	if err := os.WriteFile(rc, []byte("alias custom-b='claude'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cached, err := n.launchers(context.Background())
	if err != nil || len(cached) != 1 || cached[0].Name != "custom-a" {
		t.Fatalf("ordinary read did not use the cache: %v, %v", cached, err)
	}
	refreshed, err := n.launchers(context.Background(), true)
	if err != nil || len(refreshed) != 1 || refreshed[0].Name != "custom-b" {
		t.Fatalf("refresh did not replace discovery: %v, %v", refreshed, err)
	}
	cached, err = n.launchers(context.Background())
	if err != nil || len(cached) != 1 || cached[0].Name != "custom-b" {
		t.Fatalf("refreshed list was not cached: %v, %v", cached, err)
	}
}

func TestLaunchersQueuedRefreshUsesDiscoveryCompletedWhileWaiting(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("no zsh")
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep")
	}
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, program := range map[string]string{"zsh": zsh, "sleep": sleep} {
		if err := os.Symlink(program, filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	rc := filepath.Join(home, ".zshrc")
	if err := os.WriteFile(rc, []byte("alias custom-a='claude'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", home)
	t.Setenv("SHELL", zsh)
	t.Setenv("PATH", bin)
	launcherCache.Lock()
	at, list, generation := launcherCache.at, launcherCache.list, launcherCache.generation
	codex, settings, scripts := launcherCache.codex, launcherCache.settings, launcherCache.scripts
	launcherCache.at, launcherCache.list = time.Time{}, nil
	launcherCache.Unlock()
	t.Cleanup(func() {
		launcherCache.Lock()
		launcherCache.at, launcherCache.list, launcherCache.generation = at, list, generation
		launcherCache.codex, launcherCache.settings, launcherCache.scripts = codex, settings, scripts
		launcherCache.Unlock()
	})

	n := &Node{}
	if _, err := n.launchers(context.Background()); err != nil {
		t.Fatal(err)
	}
	started, release := filepath.Join(home, "discovery-started"), filepath.Join(home, "discovery-release")
	gated := "alias custom-b='claude'\nprint -r -- started > \"$HOME/discovery-started\"\n" +
		"while [[ ! -f \"$HOME/discovery-release\" ]]; do sleep 0.02; done\n"
	if err := os.WriteFile(rc, []byte(gated), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.WriteFile(release, []byte("release\n"), 0o600) })
	type answer struct {
		list []proto.Launcher
		err  error
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	firstDone := make(chan answer, 1)
	go func() {
		list, err := n.launchers(ctx, true)
		firstDone <- answer{list: list, err: err}
	}()
	waitFor(t, "the first discovery to start", func() bool {
		if _, err := os.Stat(started); err == nil {
			return true
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		return false
	})
	cancel() // The RPC caller may give up; node discovery deliberately finishes and publishes its result.
	if err := os.WriteFile(rc, []byte("alias custom-c='claude'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	secondDone := make(chan answer, 1)
	queued := make(chan struct{})
	secondCtx := context.WithValue(context.Background(), launcherFetchObserverKey{}, func() { close(queued) })
	go func() {
		list, err := n.launchers(secondCtx, true)
		secondDone <- answer{list: list, err: err}
	}()
	// Keep the first shell blocked until the queued refresh has recorded the old generation and reached the fetch lock.
	select {
	case <-queued:
	case <-time.After(5 * time.Second):
		t.Fatal("the second refresh did not queue for discovery")
	}
	if err := os.WriteFile(release, []byte("release\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i, done := range []chan answer{firstDone, secondDone} {
		select {
		case got := <-done:
			if got.err != nil || len(got.list) != 1 || got.list[0].Name != "custom-b" {
				t.Fatalf("discovery %d = %v, %v; want the result completed while queued", i+1, got.list, got.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("discovery %d did not finish", i+1)
		}
	}
}

func TestLaunchersRejectUnframedDiscovery(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("no zsh")
	}
	for _, exit := range []string{"0", "7"} {
		t.Run("exit-"+exit, func(t *testing.T) {
			home := t.TempDir()
			bin := filepath.Join(home, "bin")
			if err := os.Mkdir(bin, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(zsh, filepath.Join(bin, "zsh")); err != nil {
				t.Fatal(err)
			}
			// Exiting from the rc file prevents launcherScript from printing even its begin marker.
			if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("print -r -- unframed\nexit "+exit+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", home)
			t.Setenv("ZDOTDIR", home)
			t.Setenv("SHELL", zsh)
			t.Setenv("PATH", bin)
			launcherCache.Lock()
			at, list, generation := launcherCache.at, launcherCache.list, launcherCache.generation
			codex, settings, scripts := launcherCache.codex, launcherCache.settings, launcherCache.scripts
			launcherCache.at = time.Now()
			launcherCache.list = []proto.Launcher{{Name: "custom-a"}}
			observed := launcherCache.generation
			launcherCache.Unlock()
			t.Cleanup(func() {
				launcherCache.Lock()
				launcherCache.at, launcherCache.list, launcherCache.generation = at, list, generation
				launcherCache.codex, launcherCache.settings, launcherCache.scripts = codex, settings, scripts
				launcherCache.Unlock()
			})

			got, err := (&Node{}).launchers(context.Background(), true)
			if err == nil || got != nil {
				t.Fatalf("unframed discovery was published: %v, %v", got, err)
			}
			launcherCache.Lock()
			cached, currentGeneration := launcherCache.list, launcherCache.generation
			launcherCache.Unlock()
			if len(cached) != 1 || cached[0].Name != "custom-a" || currentGeneration != observed {
				t.Fatalf("failed discovery replaced the cache: %v, generation %d -> %d", cached, observed, currentGeneration)
			}
		})
	}
}

func TestLaunchersCanceledQueuedRefreshDoesNotStartAnotherShell(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("no zsh")
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep")
	}
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, program := range map[string]string{"zsh": zsh, "sleep": sleep} {
		if err := os.Symlink(program, filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	started, release := filepath.Join(home, "discovery-started"), filepath.Join(home, "discovery-release")
	starts := filepath.Join(home, "discovery-starts")
	rc := "print -r -- started > \"$HOME/discovery-started\"\nprint -r -- start >> \"$HOME/discovery-starts\"\n" +
		"while [[ ! -f \"$HOME/discovery-release\" ]]; do sleep 0.02; done\nexit 7\n"
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte(rc), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", home)
	t.Setenv("SHELL", zsh)
	t.Setenv("PATH", bin)
	launcherCache.Lock()
	at, list, generation := launcherCache.at, launcherCache.list, launcherCache.generation
	codex, settings, scripts := launcherCache.codex, launcherCache.settings, launcherCache.scripts
	launcherCache.at, launcherCache.list = time.Time{}, nil
	launcherCache.Unlock()
	t.Cleanup(func() {
		launcherCache.Lock()
		launcherCache.at, launcherCache.list, launcherCache.generation = at, list, generation
		launcherCache.codex, launcherCache.settings, launcherCache.scripts = codex, settings, scripts
		launcherCache.Unlock()
		_ = os.WriteFile(release, []byte("release\n"), 0o600)
	})

	n := &Node{}
	firstDone := make(chan error, 1)
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	go func() {
		_, err := n.launchers(firstCtx, true)
		firstDone <- err
	}()
	waitFor(t, "the first discovery to start", func() bool {
		if _, err := os.Stat(started); err == nil {
			return true
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		return false
	})
	cancelFirst() // this discovery still finishes, but fails without publishing a generation
	secondCtx, cancelSecond := context.WithCancel(context.Background())
	queued := make(chan struct{})
	secondCtx = context.WithValue(secondCtx, launcherFetchObserverKey{}, func() { close(queued) })
	secondDone := make(chan error, 1)
	go func() {
		_, err := n.launchers(secondCtx, true)
		secondDone <- err
	}()
	select {
	case <-queued:
	case <-time.After(5 * time.Second):
		t.Fatal("the second refresh did not queue for discovery")
	}
	cancelSecond()
	if err := os.WriteFile(release, []byte("release\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := <-firstDone; err == nil {
		t.Fatal("the failed first discovery unexpectedly succeeded")
	}
	select {
	case err := <-secondDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled queued refresh started another discovery: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled queued refresh did not return")
	}
	out, err := os.ReadFile(starts)
	if err != nil || string(out) != "start\n" {
		t.Fatalf("shell starts = %q, %v; want only the first discovery", out, err)
	}
}

func TestLaunchersListShellDiscoveryErrorWithRuntimeOnlyWhenForced(t *testing.T) {
	n, _ := runtimeNode(t)
	dialRuntime(t, n, dshHello())
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "bash")
	t.Setenv("PATH", home) // neither zsh nor bash can be found, so shell discovery fails immediately
	launcherCache.Lock()
	at, list, generation := launcherCache.at, launcherCache.list, launcherCache.generation
	codex, settings, scripts := launcherCache.codex, launcherCache.settings, launcherCache.scripts
	launcherCache.at, launcherCache.list = time.Time{}, nil
	launcherCache.Unlock()
	t.Cleanup(func() {
		launcherCache.Lock()
		launcherCache.at, launcherCache.list, launcherCache.generation = at, list, generation
		launcherCache.codex, launcherCache.settings, launcherCache.scripts = codex, settings, scripts
		launcherCache.Unlock()
	})
	if _, err := nodeCall(t, n, "launchers.list", map[string]any{"refresh": true}); err == nil {
		t.Fatal("a runtime overlay hid the forced shell discovery error")
	}
	raw, err := nodeCall(t, n, "launchers.list", map[string]any{})
	if err != nil {
		t.Fatalf("an ordinary picker lost its independently usable runtime: %v", err)
	}
	var listed struct {
		Launchers []proto.Launcher `json:"launchers"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(listed.Launchers, func(l proto.Launcher) bool { return l.Name == "dsh" }) {
		t.Fatalf("ordinary picker lost the runtime after shell discovery failed: %s", raw)
	}
}

// A read of the login shell that ends before the script runs (a slow rc file cut off by the cap, an rc file that exits)
// prints only what the rc files print on the way. That is no launcher list: an ordinary read keeps the last one instead
// of plain claude alone for a minute (#11580); with no list yet, the read is an error. Forced recovery reads are covered
// separately above and must return the error even when an older list exists.
func TestLaunchersKeepTheLastListWhenTheShellEndsBeforeListing(t *testing.T) {
	if _, err := exec.LookPath("zsh"); err != nil {
		t.Skip("no zsh")
	}
	home := t.TempDir()
	rc := filepath.Join(home, ".zshrc")
	write := func(defs string) {
		t.Helper()
		if err := os.WriteFile(rc, []byte(defs), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("ZDOTDIR", home)
	t.Setenv("SHELL", "/bin/zsh")

	launcherCache.Lock()
	at, list := launcherCache.at, launcherCache.list
	codex, settings, scripts := launcherCache.codex, launcherCache.settings, launcherCache.scripts
	launcherCache.at, launcherCache.list = time.Time{}, nil
	launcherCache.Unlock()
	t.Cleanup(func() {
		launcherCache.Lock()
		launcherCache.at, launcherCache.list = at, list
		launcherCache.codex, launcherCache.settings, launcherCache.scripts = codex, settings, scripts
		launcherCache.Unlock()
	})
	expire := func() {
		launcherCache.Lock()
		launcherCache.at = time.Time{}
		launcherCache.Unlock()
	}
	n := &Node{}
	ctx := context.Background()
	ending := "echo 'Welcome back'\nexit 1\n"

	write(ending)
	if ls, err := n.launchers(ctx); err == nil {
		t.Fatalf("no list yet and the shell ended before listing: launchers %v, want an error", ls)
	}

	write("_claude_launchers() { printf '%-26s%-40s%s\\n' mycc - demo-model; }\n")
	ls, err := n.launchers(ctx)
	if err != nil || !slices.ContainsFunc(ls, func(l proto.Launcher) bool { return l.Name == "mycc" }) {
		t.Fatalf("launchers %v, error %v; want mycc", ls, err)
	}

	expire()
	write(ending)
	ls, err = n.launchers(ctx)
	if err != nil || !slices.ContainsFunc(ls, func(l proto.Launcher) bool { return l.Name == "mycc" }) {
		t.Fatalf("after a read that ended before listing: launchers %v, error %v; want the last list with mycc", ls, err)
	}
	if !n.knownLauncher(ctx, "mycc") {
		t.Error("mycc is unknown after a read that ended before listing; want it still known")
	}
}
