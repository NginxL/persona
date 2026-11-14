package hub

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// fsClient is a connected browser of the test hub.
func fsClient(h *Hub, role string) *client {
	c := termClient(h, role)
	h.mu.Lock()
	if h.clients == nil {
		h.clients = map[*client]struct{}{}
	}
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	return c
}

// lastFsWatch waits for the node's n-th fs.watch and returns its params.
func lastFsWatch(t *testing.T, tn *termNode, n int) fsWatchParam {
	t.Helper()
	var got fsWatchParam
	eventually(t, 3*time.Second, "the node's fs.watch call", func() bool {
		tn.mu.Lock()
		count := 0
		for _, l := range tn.log {
			if l == "fs.watch" {
				count++
			}
		}
		raw := tn.args["fs.watch"]
		tn.mu.Unlock()
		if count < n {
			return false
		}
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatal(err)
		}
		return true
	})
	return got
}

// waitLatestFsWatch waits for the latest union the node received to satisfy ok, without requiring intermediate calls:
// changes made while one call is out coalesce into one trailing call.
func waitLatestFsWatch(t *testing.T, tn *termNode, ok func(fsWatchParam) bool) fsWatchParam {
	t.Helper()
	var got fsWatchParam
	eventually(t, 3*time.Second, "the node's latest fs.watch union", func() bool {
		tn.mu.Lock()
		raw, exists := tn.args["fs.watch"]
		tn.mu.Unlock()
		if !exists {
			return false
		}
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatal(err)
		}
		return ok(got)
	})
	return got
}

func waitFSWatchIdle(t *testing.T, m *Machine) {
	t.Helper()
	eventually(t, 3*time.Second, "the fs.watch worker to finish", func() bool {
		m.fswMu.Lock()
		defer m.fswMu.Unlock()
		return !m.fswRunning && !m.fswPending
	})
}

func fsWatchCalls(tn *termNode) int {
	tn.mu.Lock()
	defer tn.mu.Unlock()
	n := 0
	for _, l := range tn.log {
		if l == "fs.watch" {
			n++
		}
	}
	return n
}

func nextFsChanged(c *client) map[string]any {
	select {
	case b := <-c.out:
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		return m
	case <-time.After(2 * time.Second):
		return nil
	}
}

// An answer-only device cannot list folders, so it cannot ask to be told when they change.
func TestFsWatchNeedsAFullDevice(t *testing.T) {
	tn := newTermNode(t)
	h := tn.hub
	answer := fsClient(h, roleAnswer)
	say(h, answer, map[string]any{"type": "fs_watch", "machine_id": "m1", "cwd": "/w", "paths": []string{"/w"}, "git": true})
	if n := fsWatchCalls(tn); n != 0 {
		t.Fatalf("the node was asked to watch for an answer-only device (%d calls)", n)
	}
	if len(answer.fsw) != 0 {
		t.Fatalf("recorded: %+v", answer.fsw)
	}
}

// The node gets the union of the browsers' folders, replaced as a whole; the last browser leaving empties it.
func TestFsWatchUnionAndRelease(t *testing.T) {
	tn := newTermNode(t)
	h := tn.hub
	a, b := fsClient(h, roleFull), fsClient(h, roleFull)

	say(h, a, map[string]any{"type": "fs_watch", "machine_id": "m1", "cwd": "/w", "paths": []string{"/w", "/w/src"}, "git": true})
	got := lastFsWatch(t, tn, 1)
	want := fsWatchParam{Dirs: []fsWatchDirParam{{Path: "/w", Cwd: "/w"}, {Path: "/w/src", Cwd: "/w"}}, Git: []string{"/w"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("first union = %+v", got)
	}

	// b opens one of the same folders and another: the shared one is sent once.
	say(h, b, map[string]any{"type": "fs_watch", "machine_id": "m1", "cwd": "/w", "paths": []string{"/w/src", "/w/doc"}})
	got = lastFsWatch(t, tn, 2)
	if len(got.Dirs) != 3 || len(got.Git) != 1 {
		t.Fatalf("second union = %+v", got)
	}

	// a replaces its set with nothing (its panels closed): b's folders stay, the Git half goes.
	say(h, a, map[string]any{"type": "fs_watch", "machine_id": "m1", "cwd": "/w", "paths": []string{}})
	got = lastFsWatch(t, tn, 3)
	if len(got.Dirs) != 2 || len(got.Git) != 0 {
		t.Fatalf("after a left = %+v", got)
	}

	// b's connection ends: nothing is watched, and the node is told so once.
	h.dropClient(b)
	got = lastFsWatch(t, tn, 4)
	if len(got.Dirs) != 0 || len(got.Git) != 0 {
		t.Fatalf("after b dropped = %+v", got)
	}
	// With nothing watched and the node told, more empty sets send nothing once the queued worker has finished.
	say(h, a, map[string]any{"type": "fs_watch", "machine_id": "m1", "cwd": "/w", "paths": []string{}})
	waitFSWatchIdle(t, tn.where)
	if n := fsWatchCalls(tn); n != 4 {
		t.Fatalf("%d fs.watch calls, want 4", n)
	}
}

// A browser that opened folders with the id an absorbed hub.json entry used to have reaches the computer, so the
// node is told to watch them and the changes come back to it under the id it asked with: the set is kept under the
// computer's own id, the event echoes the browser's.
func TestFsWatchByAbsorbedAliasReachesTheNodeAndRoutesChanges(t *testing.T) {
	h := newTestHub(t)
	var mu sync.Mutex
	var watched []fsWatchParam
	boardNode(t, h, "canonical", func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if method == "fs.watch" {
			var p fsWatchParam
			_ = json.Unmarshal(params, &p)
			mu.Lock()
			watched = append(watched, p)
			mu.Unlock()
		}
		return map[string]any{"ok": true}, nil
	})
	h.devices.absorbed = map[string]string{"alias": "canonical"}
	c := fsClient(h, roleFull)
	say(h, c, map[string]any{"type": "fs_watch", "machine_id": "alias", "cwd": "/w", "paths": []string{"/w"}, "git": true})

	eventually(t, 3*time.Second, "the alias fs.watch call", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(watched) == 1
	})
	mu.Lock()
	got := append([]fsWatchParam(nil), watched...)
	mu.Unlock()
	want := fsWatchParam{Dirs: []fsWatchDirParam{{Path: "/w", Cwd: "/w"}}, Git: []string{"/w"}}
	if len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("the node was told %+v, want one fs.watch %+v", got, want)
	}

	h.fsChanged("canonical", []string{"/w"}, nil)
	m := nextFsChanged(c)
	if m == nil || m["type"] != "fs_changed" || m["machine_id"] != "alias" || m["cwd"] != "/w" ||
		!reflect.DeepEqual(m["dirs"], []any{"/w"}) {
		t.Fatalf("the browser was sent %+v", m)
	}

	// Closing the folder by the alias empties the node's set after the worker's trailing sync.
	say(h, c, map[string]any{"type": "fs_watch", "machine_id": "alias", "cwd": "/w", "paths": []string{}})
	eventually(t, 3*time.Second, "the empty alias fs.watch call", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(watched) == 2
	})
	mu.Lock()
	last := watched[len(watched)-1]
	mu.Unlock()
	if len(last.Dirs) != 0 || len(last.Git) != 0 {
		t.Fatalf("after closing by the alias the node was told %+v", last)
	}
	waitFSWatchIdle(t, h.localMachine("canonical"))
}

// One page that watches a folder by both the absorbed id and the computer's own one (a terminal route opened by the
// alias next to a Files panel from the session list) keeps both: each panel hears changes under the id it asked with,
// and closing one leaves the other watching.
func TestFsWatchByBothIDsOfOneComputerKeepsEach(t *testing.T) {
	h := newTestHub(t)
	var mu sync.Mutex
	var watched []fsWatchParam
	boardNode(t, h, "canonical", func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if method == "fs.watch" {
			var p fsWatchParam
			_ = json.Unmarshal(params, &p)
			mu.Lock()
			watched = append(watched, p)
			mu.Unlock()
		}
		return map[string]any{"ok": true}, nil
	})
	h.devices.absorbed = map[string]string{"alias": "canonical"}
	c := fsClient(h, roleFull)
	say(h, c, map[string]any{"type": "fs_watch", "machine_id": "canonical", "cwd": "/w", "paths": []string{"/w"}})
	say(h, c, map[string]any{"type": "fs_watch", "machine_id": "alias", "cwd": "/w", "paths": []string{"/w/a"}})

	asked := func() map[string]any {
		t.Helper()
		got := map[string]any{}
		for range 2 {
			m := nextFsChanged(c)
			if m == nil || m["type"] != "fs_changed" {
				t.Fatalf("the browser was sent %+v, want fs_changed", m)
			}
			got[m["machine_id"].(string)] = m["dirs"]
		}
		if m := nextFsChangedNow(c); m != nil {
			t.Fatalf("one more message %+v", m)
		}
		return got
	}
	h.fsChanged("canonical", []string{"/w", "/w/a"}, nil)
	if got, want := asked(), map[string]any{"canonical": []any{"/w"}, "alias": []any{"/w/a"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fs_changed by id = %v, want %v", got, want)
	}

	say(h, c, map[string]any{"type": "fs_watch", "machine_id": "alias", "cwd": "/w", "paths": []string{}})
	want := fsWatchParam{Dirs: []fsWatchDirParam{{Path: "/w", Cwd: "/w"}}}
	eventually(t, 3*time.Second, "the canonical set after the alias panel closed", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(watched) > 0 && reflect.DeepEqual(watched[len(watched)-1], want)
	})
	h.fsChanged("canonical", []string{"/w", "/w/a"}, nil)
	m := nextFsChanged(c)
	if m == nil || m["machine_id"] != "canonical" || !reflect.DeepEqual(m["dirs"], []any{"/w"}) {
		t.Fatalf("after the alias panel closed the browser was sent %+v", m)
	}
	waitFSWatchIdle(t, h.localMachine("canonical"))
}

// fs.changed reaches only the browsers whose folders it touches, and says which.
func TestFsChangedRoutesToTheBrowsersThatHaveItOpen(t *testing.T) {
	tn := newTermNode(t)
	h := tn.hub
	a, b, c := fsClient(h, roleFull), fsClient(h, roleFull), fsClient(h, roleFull)
	say(h, a, map[string]any{"type": "fs_watch", "machine_id": "m1", "cwd": "/w", "paths": []string{"/w", "/w/src"}, "git": true})
	say(h, b, map[string]any{"type": "fs_watch", "machine_id": "m1", "cwd": "/other", "paths": []string{"/other"}})
	say(h, c, map[string]any{"type": "fs_watch", "machine_id": "m1", "cwd": "/w", "paths": []string{"/w/doc"}})

	h.byID["m1"].onEvent("fs.changed", json.RawMessage(`{"dirs":["/w/src"],"git":["/w"]}`))

	m := nextFsChanged(a)
	if m == nil || m["type"] != "fs_changed" || m["machine_id"] != "m1" || m["cwd"] != "/w" || m["git"] != true ||
		!reflect.DeepEqual(m["dirs"], []any{"/w/src"}) {
		t.Fatalf("a was sent %+v", m)
	}
	if m := nextFsChangedNow(b); m != nil {
		t.Fatalf("b has other folders open but was sent %+v", m)
	}
	if m := nextFsChangedNow(c); m != nil {
		t.Fatalf("c does not show Git and has other folders but was sent %+v", m)
	}
	waitFSWatchIdle(t, tn.where)
}

func nextFsChangedNow(c *client) map[string]any {
	select {
	case b := <-c.out:
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		return m
	default:
		return nil
	}
}

// A browser cannot make the hub carry unbounded sets.
func TestCleanFsWatchBoundsWhatABrowserSends(t *testing.T) {
	var paths []string
	for i := 0; i < maxFsWatchPaths+20; i++ {
		paths = append(paths, "/w/d"+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	paths = append(paths, "", paths[0], strings.Repeat("x", maxFsPathLen+1))
	r := cleanFsWatch("/w", paths, true)
	if len(r.Paths) != maxFsWatchPaths {
		t.Fatalf("%d paths kept", len(r.Paths))
	}
	if !cleanFsWatch("", paths, true).empty() {
		t.Fatal("a watch without a cwd was kept")
	}
}

// Two session folders on one machine are two sets of one browser: neither replaces the other.
func TestFsWatchKeepsEachSessionFolderApart(t *testing.T) {
	tn := newTermNode(t)
	h := tn.hub
	a := fsClient(h, roleFull)
	say(h, a, map[string]any{"type": "fs_watch", "machine_id": "m1", "cwd": "/w1", "paths": []string{"/w1"}})
	say(h, a, map[string]any{"type": "fs_watch", "machine_id": "m1", "cwd": "/w2", "paths": []string{"/w2"}, "git": true})
	got := waitLatestFsWatch(t, tn, func(p fsWatchParam) bool { return len(p.Dirs) == 2 && len(p.Git) == 1 })
	if got.Git[0] != "/w2" {
		t.Fatalf("union = %+v", got)
	}
	say(h, a, map[string]any{"type": "fs_watch", "machine_id": "m1", "cwd": "/w1", "paths": []string{}})
	got = waitLatestFsWatch(t, tn, func(p fsWatchParam) bool { return len(p.Dirs) == 1 && p.Dirs[0].Path == "/w2" })
	if len(got.Git) != 1 {
		t.Fatalf("after /w1 closed = %+v", got)
	}
	waitFSWatchIdle(t, tn.where)
}

// A node whose connection is new to the hub is told the union again, an empty one too: it may still hold a set an
// earlier hub gave it that nobody shows any more.
func TestFsWatchReconnectedTellsTheNodeEvenWhenNothingIsOpen(t *testing.T) {
	tn := newTermNode(t)
	h := tn.hub
	h.fsWatchReconnected(tn.where)
	got := lastFsWatch(t, tn, 1)
	if len(got.Dirs) != 0 || len(got.Git) != 0 {
		t.Fatalf("union = %+v", got)
	}
	a := fsClient(h, roleFull)
	say(h, a, map[string]any{"type": "fs_watch", "machine_id": "m1", "cwd": "/w", "paths": []string{"/w"}})
	lastFsWatch(t, tn, 2)
	h.fsWatchReconnected(tn.where) // a new connection: the open folders again
	got = lastFsWatch(t, tn, 3)
	if len(got.Dirs) != 1 {
		t.Fatalf("after reconnect = %+v", got)
	}
	waitFSWatchIdle(t, tn.where)
}
