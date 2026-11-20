package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// Deleting a conversation takes an offline one out of what the apps get (the list, the pushes), kept across a
// restart; a running one, the assistant's and an unknown one can't be deleted; used again, it is listed again.
func TestDeleteSessions(t *testing.T) {
	h, n := conversationsHub(t)
	now := time.Now().UTC()
	editSessions(n.m, func(list []proto.Session) []proto.Session {
		return append(list,
			proto.Session{SessionID: "old-0007", Cwd: "/code/app", Status: proto.StatusOffline, UpdatedAt: now.Add(-time.Hour)},
			proto.Session{SessionID: "fail-0008", Cwd: "/code/app", Status: proto.StatusOffline, UpdatedAt: now.Add(-time.Minute),
				StartError: "claude did not become ready within 1m0s"},
			proto.Session{SessionID: "run-0009", Cwd: "/code/app", Status: proto.StatusWorking, UpdatedAt: now},
			proto.Session{SessionID: "boot-0010", Cwd: "/code/app", Status: proto.StatusOffline, Starting: true, UpdatedAt: now},
			proto.Session{SessionID: "done-0011", Cwd: "/code/app", Status: proto.StatusDone, UpdatedAt: now.Add(-time.Hour)},
			proto.Session{SessionID: "busy-0012", Cwd: "/code/app", Status: proto.StatusBlocked, UpdatedAt: now},
			proto.Session{SessionID: "stopped-0013", Cwd: "/code/app", Status: proto.StatusInterrupted, UpdatedAt: now.Add(-time.Hour)},
			proto.Session{SessionID: "keep-0014", Cwd: "/code/app", Status: proto.StatusInterrupted, UpdatedAt: now.Add(-time.Hour)},
		)
	})
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /api/v1/sessions/{mid}/{sid}", h.withMachine(h.deleteSession))
	mux.HandleFunc("GET /api/v1/sessions", h.listSessions)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	del := func(path string) int {
		req, _ := http.NewRequest(http.MethodDelete, srv.URL+path, nil)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		return res.StatusCode
	}
	listed := func() string {
		res, err := http.Get(srv.URL + "/api/v1/sessions")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var v struct {
			Sessions []proto.Session `json:"sessions"`
		}
		_ = json.NewDecoder(res.Body).Decode(&v)
		return ids(v.Sessions)
	}

	for _, id := range []string{"old-0007", "fail-0008", "done-0011", "stopped-0013"} {
		if c := del("/api/v1/sessions/server/" + id); c != http.StatusOK {
			t.Fatalf("delete %s: %d", id, c)
		}
	}
	got := listed()
	if strings.Contains(got, "old-0007") || strings.Contains(got, "fail-0008") || strings.Contains(got, "done-0011") || !strings.Contains(got, "run-0009") {
		t.Fatalf("deleted ones are left out of the list: %s", got)
	}
	if got := ids(h.shown("server", h.decorated(n.m))); strings.Contains(got, "old-0007") {
		t.Fatalf("and out of the pushes: %s", got)
	}
	if got := ids(h.decorated(n.m)); !strings.Contains(got, "old-0007") {
		t.Fatalf("the hub itself still knows it: %s", got)
	}
	if strings.Contains(got, "stopped-0013") || !strings.Contains(got, "keep-0014") {
		t.Fatalf("only the deleted interrupted session is hidden: %s", got)
	}
	for path, want := range map[string]int{
		"/api/v1/sessions/server/run-0009":  http.StatusConflict,
		"/api/v1/sessions/server/boot-0010": http.StatusConflict,
		"/api/v1/sessions/server/busy-0012": http.StatusConflict,
		"/api/v1/sessions/server/trip-0002": http.StatusBadRequest, // the assistant's: archived instead
		"/api/v1/sessions/server/nope-0000": http.StatusNotFound,
		"/api/v1/sessions/elsewhere/x":      http.StatusNotFound,
	} {
		if c := del(path); c != want {
			t.Fatalf("delete %s: %d, want %d", path, c, want)
		}
	}

	// Kept across a restart (the database's settings, not memory).
	h2 := newTestHub(t)
	h2.store = h.store
	if _, ok := h2.deletedAt("server")["old-0007"]; !ok {
		t.Fatal("not kept")
	}
	// A node restart may classify an unchanged offline transcript as interrupted.
	// Reloaded deletion notes must still filter both HTTP lists and socket snapshots.
	editSessions(n.m, func(list []proto.Session) []proto.Session {
		for i := range list {
			if list[i].SessionID == "old-0007" {
				list[i].Status = proto.StatusInterrupted
			}
		}
		return list
	})
	for _, rows := range []string{listed(), ids(h2.shown("server", h.decorated(n.m)))} {
		if strings.Contains(rows, "old-0007") || strings.Contains(rows, "stopped-0013") || !strings.Contains(rows, "keep-0014") {
			t.Fatalf("reconnected lists retain deletion by ID: %s", rows)
		}
	}

	// Used again (resumed, a new message): listed again.
	editSessions(n.m, func(list []proto.Session) []proto.Session {
		for i := range list {
			if list[i].SessionID == "old-0007" {
				list[i].UpdatedAt = time.Now().UTC().Add(time.Second)
			}
		}
		return list
	})
	if got := listed(); !strings.Contains(got, "old-0007") || strings.Contains(got, "fail-0008") {
		t.Fatalf("a session used again is back: %s", got)
	}
}

// An untouched start can be removed by the Hub while the machine is offline. The inventory remains the node's.
func TestRemoveUntouchedStartWithoutNode(t *testing.T) {
	h, n := conversationsHub(t)
	const sid = "ghost-0026"
	row := proto.Session{SessionID: sid, Cwd: "/work", Status: proto.StatusUnknown, StartedByPersona: true, PaneID: "w1:p1"}
	editSessions(n.m, func(list []proto.Session) []proto.Session { return append(list, row) })
	n.m.mu.Lock()
	n.m.peer = nil
	n.m.online = false
	n.m.mu.Unlock()

	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /api/v1/sessions/{mid}/{sid}", h.withMachine(h.deleteSession))
	remove := func(role string) int {
		req := httptest.NewRequest(http.MethodDelete, "/api/v1/sessions/server/"+sid, nil)
		req = req.WithContext(context.WithValue(req.Context(), principalKey{}, principal{Role: role}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w.Code
	}
	if got := remove(roleAnswer); got != http.StatusForbidden {
		t.Fatalf("restricted device removed session: %d", got)
	}
	if got := remove(roleFull); got != http.StatusOK {
		t.Fatalf("offline list removal: %d", got)
	}
	if strings.Contains(ids(h.shown("server", h.decorated(n.m))), sid) {
		t.Fatal("removed session still listed")
	}
	if _, found := h.decoratedSession(n.m, sid); !found {
		t.Fatal("Hub forgot the node's inventory")
	}
	row.UpdatedAt = time.Now().Add(time.Second)
	if len(h.shown("server", []proto.Session{row})) != 1 {
		t.Fatal("new activity did not revive the row")
	}
}

// Old notes remain: manager snapshots can outlive the node inventory indefinitely.
func TestDeletedNotesDoNotExpire(t *testing.T) {
	h, _ := conversationsHub(t)
	now := time.Now().UTC()
	if err := h.noteDeleted(t.Context(), "server", "a", now.Add(-365*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := h.noteDeleted(t.Context(), "server", "b", now); err != nil {
		t.Fatal(err)
	}
	got := h.deletedAt("server")
	if _, ok := got["a"]; !ok || len(got) != 2 {
		t.Fatalf("old deletion lost: %v", got)
	}
}

// A finished session still open on its computer (idle: seen, a Codex App thread the App lists for days) can be
// deleted too: it stays out of the lists whatever its finished status reads next (done, idle, unknown), across a
// reload, and comes back only when it works again, is used again or asks the user something.
func TestDeleteIdleSessions(t *testing.T) {
	h, n := conversationsHub(t)
	now := time.Now().UTC()
	desk := proto.Session{SessionID: "desk-0021", Cwd: "/code/app", Status: proto.StatusIdle, UpdatedAt: now.Add(-2 * time.Hour),
		Agent: "codex", SourceKind: "codex_history", SpaceKind: "codex_app", Capabilities: &proto.SessionCapabilities{Read: true, Send: true}}
	editSessions(n.m, func(list []proto.Session) []proto.Session {
		return append(list,
			proto.Session{SessionID: "seen-0020", Cwd: "/code/app", Status: proto.StatusIdle, UpdatedAt: now.Add(-time.Hour)},
			desk,
			proto.Session{SessionID: "open-0022", Cwd: "/code/app", Status: proto.StatusIdle, UpdatedAt: now.Add(-time.Hour)},
		)
	})
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /api/v1/sessions/{mid}/{sid}", h.withMachine(h.deleteSession))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	for _, id := range []string{"seen-0020", "desk-0021"} {
		req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/api/v1/sessions/server/"+id, nil)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("delete %s: %d", id, res.StatusCode)
		}
	}
	set := func(id string, f func(*proto.Session)) {
		editSessions(n.m, func(list []proto.Session) []proto.Session {
			for i := range list {
				if list[i].SessionID == id {
					f(&list[i])
				}
			}
			return list
		})
	}
	shown := func() string { return ids(h.shown("server", h.decorated(n.m))) }
	if got := shown(); strings.Contains(got, "seen-0020") || strings.Contains(got, "desk-0021") || !strings.Contains(got, "open-0022") {
		t.Fatalf("deleted idle sessions leave the list, the others stay: %s", got)
	}
	// Its finished status changing with no new work (the App marks it unread, then seen; a restart reads unknown) does
	// not bring it back, nor does a reload of the notes.
	for _, st := range []string{proto.StatusDone, proto.StatusUnknown, proto.StatusIdle} {
		set("desk-0021", func(s *proto.Session) { s.Status = st })
		if got := shown(); strings.Contains(got, "desk-0021") {
			t.Fatalf("status %s brought it back: %s", st, got)
		}
	}
	h2 := newTestHub(t)
	h2.store = h.store
	if got := ids(h2.shown("server", h.decorated(n.m))); strings.Contains(got, "seen-0020") || strings.Contains(got, "desk-0021") {
		t.Fatalf("kept across a restart: %s", got)
	}
	// A turn running brings it back while it runs, and the turn's newer activity keeps it back.
	set("desk-0021", func(s *proto.Session) { s.Status = proto.StatusWorking })
	if got := shown(); !strings.Contains(got, "desk-0021") {
		t.Fatalf("a working session is listed: %s", got)
	}
	set("desk-0021", func(s *proto.Session) { s.Status, s.UpdatedAt = proto.StatusDone, time.Now().UTC().Add(time.Second) })
	if got := shown(); !strings.Contains(got, "desk-0021") {
		t.Fatalf("used again, it stays listed: %s", got)
	}
	// A question waiting for the user brings it back too.
	set("seen-0020", func(s *proto.Session) { s.PendingInteractions = 1 })
	if got := shown(); !strings.Contains(got, "seen-0020") {
		t.Fatalf("a session waiting for an answer is listed: %s", got)
	}
}

// shown hands back the list itself when it leaves nothing out (most pushes: a copy was ~200 KB for a busy machine), and
// otherwise a new list, the given one untouched.
func TestShownCopiesOnlyToLeaveOut(t *testing.T) {
	h := newTestHub(t)
	list := []proto.Session{{SessionID: "a"}, {SessionID: "b", Archived: true}, {SessionID: "c"}}
	if got := h.shown("box", list[:1]); len(got) != 1 || &got[0] != &list[0] {
		t.Fatalf("nothing left out, copied: %v", got)
	}
	if got := h.shown("box", list); len(got) != 2 || got[1].SessionID != "c" || &got[0] == &list[0] || list[1].SessionID != "b" {
		t.Fatalf("archived row: shown %v, list %v", got, list)
	}
}

// tombstoneHub is a hub whose saved notes hold per deleted sessions on each of the machines m0, m1, ...
func tombstoneHub(t testing.TB, machines, per int) *Hub {
	t.Helper()
	h := &Hub{store: &settingsStore{vals: map[string][]byte{}}}
	notes := map[string]time.Time{}
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for m := range machines {
		for i := range per {
			notes[fmt.Sprintf("m%d/gone-%04d", m, i)] = at
		}
	}
	if err := h.store.PutSetting(context.Background(), deletedSessionsKey, notes); err != nil {
		t.Fatal(err)
	}
	return h
}

// Tombstones never expire, so a push must not go through every machine's notes to find its own.
func TestDeletedAtLooksUpOneMachine(t *testing.T) {
	h := tombstoneHub(t, 3, 1000)
	if got := h.deletedAt("m0"); len(got) != 1000 {
		t.Fatalf("m0 has %d notes, want 1000", len(got))
	}
	if n := testing.AllocsPerRun(100, func() { h.deletedAt("m0") }); n != 0 {
		t.Fatalf("deletedAt allocates %v times a call", n)
	}
}

// The notes by machine are the saved "machine/session id" notes split at the machine: loaded, after a new note
// and reloaded. A map already handed out does not change under its reader.
func TestDeletedAtMatchesTheSavedNotes(t *testing.T) {
	ctx := context.Background()
	h := newTestHub(t)
	h.store = &settingsStore{vals: map[string][]byte{}}
	at := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	// old-entry is a hub.json entry absorbed into m1: its notes stay its own.
	notes := map[string]time.Time{"m1/a": at, "m1/b/c": at, "m10/a": at.Add(time.Minute), "m0/z": at, "old-entry/a": at, "stray": at, "/x": at}
	if err := h.store.PutSetting(ctx, deletedSessionsKey, notes); err != nil {
		t.Fatal(err)
	}
	// What deletedAt found before: every note whose key starts with "machine/".
	scan := func(machine string) map[string]time.Time {
		out := map[string]time.Time{}
		for k, at := range notes {
			if sid, ok := strings.CutPrefix(k, machine+"/"); ok {
				out[sid] = at
			}
		}
		return out
	}
	same := func(h *Hub, when string) {
		t.Helper()
		for _, machine := range []string{"m0", "m1", "m10", "m2", "old-entry", "elsewhere", "", "m1/b"} {
			if got, want := h.deletedAt(machine), scan(machine); !maps.EqualFunc(got, want, time.Time.Equal) {
				t.Fatalf("%s: deletedAt(%q) = %v, want %v", when, machine, got, want)
			}
		}
	}
	same(h, "loaded")
	list := []proto.Session{
		{SessionID: "a", Status: proto.StatusOffline, UpdatedAt: at.Add(-time.Hour)},
		{SessionID: "d", Status: proto.StatusOffline, UpdatedAt: at.Add(-time.Hour)},
	}
	if got := ids(h.shown("m1", list)); got != "d" {
		t.Fatalf("m1 shows %q, want d", got)
	}
	if got := ids(h.shown("elsewhere", list)); got != "a d" {
		t.Fatalf("a machine without notes shows %q, want all", got)
	}

	before := h.deletedAt("m1")
	for _, n := range []struct{ machine, id string }{{"m1", "d"}, {"m2", "e"}} {
		if err := h.noteDeleted(ctx, n.machine, n.id, at); err != nil {
			t.Fatal(err)
		}
		notes[n.machine+"/"+n.id] = at
	}
	if len(before) != 2 {
		t.Fatalf("a map handed out changed: %v", before)
	}
	same(h, "noted")
	if got := ids(h.shown("m1", list)); got != "" {
		t.Fatalf("m1 shows %q after the new note, want none", got)
	}
	h2 := newTestHub(t)
	h2.store = h.store
	same(h2, "reloaded")
}

// BenchmarkShownWithTombstones is a push of a machine's 150 sessions, none of them deleted, on a hub holding 3,000
// deletion notes over 3 machines.
func BenchmarkShownWithTombstones(b *testing.B) {
	h := tombstoneHub(b, 3, 1000)
	list := make([]proto.Session, 150)
	for i := range list {
		list[i] = proto.Session{SessionID: fmt.Sprintf("live-%04d", i), Status: proto.StatusOffline}
	}
	h.shown("m0", list) // loads the notes
	b.ReportAllocs()
	for b.Loop() {
		h.shown("m0", list)
	}
}
