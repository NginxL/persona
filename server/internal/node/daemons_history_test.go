package node

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/sessionhistory"
)

// daemonReady waits until the daemon id said hello through its link.
func daemonReady(t *testing.T, rh *RuntimeHost, id string) *runtimeConn {
	t.Helper()
	var h *runtimeConn
	waitFor(t, "the daemon's hello", func() bool {
		if h = daemonConn(rh, id); h == nil {
			return false
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.ready
	})
	return h
}

// A daemon that declares caps.history keeps it through its link and is listed in full, page by page, under the ids it
// has here (<daemon id>.<its id>, history_runtime the configured id); the window reaches it as it is, with no session
// id to translate, and a deleted session never comes back.
func TestDaemonHistoryIsListedInFullUnderItsIDs(t *testing.T) {
	d := startFakeDaemon(t, "tok", map[string]any{"runtime": "x", "caps": map[string]any{"send": true, "history": true}})
	now := time.Now().UTC().Add(-time.Minute) // the newest row before the window's end (until: now)
	var rows []map[string]any
	for i := 0; i < 300; i++ { // one every 6 hours: 75 days
		rows = append(rows, map[string]any{"session_id": fmt.Sprintf("s%03d", i), "status": "offline", "space_id": "w", "pane_id": "w1:p1",
			"updated_at": now.Add(-time.Duration(i) * 6 * time.Hour).Format(time.RFC3339Nano)})
	}
	rows = append(rows, map[string]any{"session_id": "gone", "status": "deleted", "updated_at": now.Format(time.RFC3339Nano)},
		map[string]any{"session_id": "../bad", "updated_at": now.Format(time.RFC3339Nano)})
	d.answer = historyRuntime(rows)
	rh, _ := daemonHost(t, map[string]any{"id": "acme", "url": d.url(), "token": "tok"})
	n, _ := runtimeNodeOn(t, rh)
	h := daemonReady(t, rh, "acme")
	h.mu.Lock()
	declared := h.hello.Caps.History
	h.mu.Unlock()
	if !declared {
		t.Fatal("the link filtered caps.history out of the daemon's hello")
	}

	since := now.Add(-90 * 24 * time.Hour).Format(time.RFC3339Nano)
	var got []sessionhistory.Row
	cursor, pages := "", 0
	for {
		ans := historyCall(t, n, map[string]any{"since": since, "source": "runtime", "limit": 100, "cursor": cursor})
		pages++
		if pages == 1 && (len(ans.Sources) != 1 || !ans.Sources[0].Honored || ans.Sources[0].Runtime != "acme" || !ans.Complete) {
			t.Fatalf("sources: %+v (complete %v)", ans.Sources, ans.Complete)
		}
		got = append(got, ans.Rows...)
		if ans.NextCursor == "" {
			break
		}
		cursor = ans.NextCursor
		if pages > 10 {
			t.Fatal("no last page")
		}
	}
	if pages != 3 || len(got) != 300 {
		t.Fatalf("%d pages, %d rows; want 3 pages of the 300", pages, len(got))
	}
	for i, r := range got {
		if want := fmt.Sprintf("acme.s%03d", i); r.Session.SessionID != want {
			t.Fatalf("row %d is %q, want %q (prefixed, newest first)", i, r.Session.SessionID, want)
		}
		if r.Runtime != "acme" || r.Source != sessionhistory.SourceRuntime || r.Session.PaneID != "" || r.Session.SpaceID != "acme:w" || r.Session.Agent != "acme" {
			t.Fatalf("row %d: %+v (runtime %q)", i, r.Session, r.Runtime)
		}
	}
	asked := d.got("sessions.history")
	if len(asked) == 0 {
		t.Fatal("the daemon was not asked")
	}
	var p map[string]any
	_ = json.Unmarshal(asked[0], &p)
	if _, named := p["session_id"]; named || p["since"] != since || p["limit"] == nil {
		t.Fatalf("the window as the daemon got it: %v", p)
	}
}

// A daemon that does not declare caps.history is never asked: marked honored false, unsupported.
func TestDaemonWithoutHistoryIsUnsupported(t *testing.T) {
	d := startFakeDaemon(t, "tok", map[string]any{"runtime": "x", "caps": map[string]any{"send": true}})
	rh, _ := daemonHost(t, map[string]any{"id": "acme", "url": d.url(), "token": "tok"})
	n, _ := runtimeNodeOn(t, rh)
	daemonReady(t, rh, "acme")
	ans := historyCall(t, n, map[string]any{"since": time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339Nano), "source": "runtime"})
	if len(ans.Sources) != 1 || ans.Sources[0].Runtime != "acme" || ans.Sources[0].Honored || ans.Sources[0].Reason != sessionhistory.ReasonUnsupported || ans.Complete {
		t.Fatalf("sources: %+v (complete %v)", ans.Sources, ans.Complete)
	}
	if len(ans.Rows) != 0 || len(d.got("sessions.history")) != 0 {
		t.Fatalf("rows %v; asked %d times", ans.Rows, len(d.got("sessions.history")))
	}
}

// A history row the daemon never listed (archived there) is read the usual way: session.items for it reaches the
// daemon under its own id, by the prefix.
func TestDaemonHistoryRowItemsGoToTheDaemon(t *testing.T) {
	d := startFakeDaemon(t, "tok", map[string]any{"runtime": "x", "caps": map[string]any{"send": true, "history": true}})
	list := historyRuntime([]map[string]any{{"session_id": "old1", "archived": true, "status": "done",
		"updated_at": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)}})
	d.answer = func(method string, params json.RawMessage) (any, error) {
		var p struct {
			SessionID string `json:"session_id"`
		}
		_ = json.Unmarshal(params, &p)
		switch {
		case method == "sessions.history":
			return list(method, params)
		case method == "session.items" && p.SessionID == "old1":
			return map[string]any{"items": []map[string]any{{"id": "1#0", "seq": 1, "role": "user", "kind": "text", "text": "from the archive"}}, "to_seq": 1, "history": "h"}, nil
		case method == "session.items":
			return nil, &rpc.Error{Message: "no session " + p.SessionID, Code: "not_found"}
		}
		return map[string]any{"ok": true}, nil
	}
	rh, _ := daemonHost(t, map[string]any{"id": "acme", "url": d.url(), "token": "tok"})
	n, _ := runtimeNodeOn(t, rh)
	d.push(t, "runtime.sessions", map[string]any{"sessions": []map[string]any{}})
	daemonReady(t, rh, "acme")

	ans := historyCall(t, n, map[string]any{"since": time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339Nano), "source": "runtime"})
	if len(ans.Rows) != 1 || ans.Rows[0].Session.SessionID != "acme.old1" || !ans.Rows[0].Session.Archived {
		t.Fatalf("history: %+v", ans.Rows)
	}
	raw, err := nodeCall(t, n, "session.items", map[string]any{"session_id": "acme.old1", "turns": 1})
	if err != nil || !strings.Contains(string(raw), "from the archive") {
		t.Fatalf("items of the history row: %s %v", raw, err)
	}
	var last map[string]any
	items := d.got("session.items")
	if len(items) == 0 {
		t.Fatal("the daemon was not asked for items")
	}
	_ = json.Unmarshal(items[len(items)-1], &last)
	if last["session_id"] != "old1" {
		t.Fatalf("the daemon got %v, want its own id old1", last)
	}
}
