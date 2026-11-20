package devices

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
)

const outboxPath = "/api/v1/machines/mac/held-messages"

func outboxRead(mux http.Handler, path, role, person string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r = r.WithContext(i18n.With(r.Context(), i18n.En))
	r.Header.Set(assistantwire.RoleHeader, role)
	r.Header.Set(assistantwire.PersonHeader, person)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func outboxPageOf(t *testing.T, mux http.Handler, path string) devicewire.HeldOutboxPage {
	t.Helper()
	result := outboxRead(mux, path, "full", "1")
	var page devicewire.HeldOutboxPage
	if result.Code != http.StatusOK || json.Unmarshal(result.Body.Bytes(), &page) != nil {
		t.Fatalf("list %s: %d %s", path, result.Code, result.Body)
	}
	for _, excluded := range []string{"fixture message body", "fixture attachment", "fixture credential", "\"text\"", "\"images\"", "\"call\""} {
		if strings.Contains(result.Body.String(), excluded) {
			t.Fatalf("list included %q: %s", excluded, result.Body)
		}
	}
	if result.Header().Get("Cache-Control") != "no-store" || page.Messages == nil {
		t.Fatalf("list headers %v, page %+v", result.Header(), page)
	}
	return page
}

func TestHeldOutboxOfflineReadAndDiscard(t *testing.T) {
	store := newHeldMem()
	d, published := heldDevices(t, store, time.Hour)
	offlineMachine(d, "mac", "Studio Mac")
	offlineMachine(d, "other", "Other computer")
	for _, m := range [][3]string{{"mac", "missing-agent", "a"}, {"other", "missing-agent", "b"}, {"mac", "second-agent", "c"}} {
		out, err := d.Call(context.Background(), heldSend(m[0], m[1], m[2], "fixture message body"))
		heldAnswerOf(t, out, err)
	}
	eventually(t, time.Second, "both holds published", func() bool { return published.count("mac") == 2 })
	before := store.all()
	updates := published.count("mac")
	handler := d.Handler(http.NotFoundHandler())
	first := outboxPageOf(t, handler, outboxPath+"?limit=1")
	if first.MachineID != "mac" || first.Computer != "Studio Mac" || first.NextCursor == "" || len(first.Messages) != 1 {
		t.Fatalf("first page %+v", first)
	}
	a := first.Messages[0]
	if a.ID != "a" || a.SessionID != "missing-agent" || a.State != "waiting" ||
		!a.CanDiscard || a.CanRetry || a.SentAt.IsZero() || a.HeldAt.IsZero() {
		t.Fatalf("waiting row %+v", a)
	}
	if got := store.all(); !reflect.DeepEqual(got, before) || published.count("mac") != updates {
		t.Fatalf("outbox inspection changed the queue: %+v", got)
	}
	// The cursor can advance even after the last row of that page is removed.
	if code, body := personCall(t, handler, http.MethodDelete, "/api/v1/sessions/mac/missing-agent/held/a"); code != http.StatusOK || body["ok"] != true {
		t.Fatalf("discard: %d %+v", code, body)
	}
	second := outboxPageOf(t, handler, outboxPath+"?limit=1&after="+url.QueryEscape(first.NextCursor))
	if len(second.Messages) != 1 || second.Messages[0].ID != "c" || second.NextCursor != "" {
		t.Fatalf("second page %+v", second)
	}
	empty := outboxPageOf(t, handler, outboxPath+"?session=missing-agent")
	if len(empty.Messages) != 0 {
		t.Fatalf("discarded row still present: %+v", empty)
	}
	for _, invalid := range []string{
		outboxPath + "?limit=0", outboxPath + "?limit=101", outboxPath + "?state=done", outboxPath + "?after=bad",
		"/api/v1/machines/other/held-messages?after=" + first.NextCursor,
		outboxPath + "?session=second-agent&after=" + first.NextCursor,
		outboxPath + "?state=waiting&after=" + first.NextCursor,
	} {
		if result := outboxRead(handler, invalid, "full", "1"); result.Code != http.StatusBadRequest {
			t.Errorf("invalid query %s: %d %s", invalid, result.Code, result.Body)
		}
	}
}

func TestHeldOutboxStatesAndFilters(t *testing.T) {
	d := newTestDevices(t)
	offlineMachine(d, "mac", "Studio Mac")
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	row := func(seq int64, id, session, state, failure string) *heldRow {
		return newHeldRow(db.HeldOperation{
			Seq: seq, MachineID: "mac", OpID: id, SessionID: session, State: state, Failure: failure,
			SentAt: now, HeldAt: now, Text: "fixture message body", Images: 1,
			Error: "refusal quoting fixture attachment", CredentialID: "fixture credential",
		})
	}
	checking := row(4, "checking", "checking-agent", db.HeldWaiting, db.HeldUnclear)
	checking.checking = true
	d.held.loaded = true
	d.held.rows = map[string][]*heldRow{"mac": {
		row(1, "unclear", "first", db.HeldFailed, db.HeldUnclear),
		row(2, "blocked", "first", db.HeldWaiting, ""),
		row(3, "sending", "sending-agent", db.HeldSending, ""),
		checking,
		row(5, "signed-out", "other-agent", db.HeldFailed, db.HeldSignedOut),
		row(6, "refused", "other-agent", db.HeldFailed, db.HeldRefused),
	}}
	handler := d.Handler(http.NotFoundHandler())
	page := outboxPageOf(t, handler, outboxPath+"?limit=1&state=waiting")
	if len(page.Messages) != 1 || page.Messages[0].ID != "blocked" ||
		!strings.Contains(page.Messages[0].Detail, "earlier message") || page.NextCursor != "" {
		t.Fatalf("unsurfaced blocker: %+v", page)
	}
	// Missing one state in the filter doesn't make a blocker vanish on a subsequent page either.
	one := outboxPageOf(t, handler, outboxPath+"?limit=1")
	two := outboxPageOf(t, handler, outboxPath+"?limit=1&after="+one.NextCursor)
	if len(two.Messages) != 1 || two.Messages[0].Detail != page.Messages[0].Detail {
		t.Fatalf("blocker before the cursor: %+v", two)
	}
	rest := outboxPageOf(t, handler, outboxPath+"?after="+two.NextCursor).Messages
	if len(rest) != 4 {
		t.Fatalf("remaining states %+v", rest)
	}
	if rest[0].State != "sending" || rest[0].CanRetry || rest[0].CanDiscard {
		t.Errorf("sending row %+v", rest[0])
	}
	if rest[1].State != "checking" || rest[1].CanRetry || !rest[1].CanDiscard {
		t.Errorf("checking row %+v", rest[1])
	}
	if rest[2].CanRetry || !rest[2].CanDiscard || rest[2].Failure != "signed_out" ||
		!rest[3].CanRetry || !rest[3].CanDiscard || rest[3].Failure != "refused" {
		t.Errorf("failed rows %+v", rest[2:])
	}
	failed := outboxPageOf(t, handler, outboxPath+"?session=other-agent&state=failed")
	if len(failed.Messages) != 2 || failed.Messages[0].ID != "signed-out" || failed.Messages[1].ID != "refused" {
		t.Fatalf("combined filters %+v", failed)
	}
}

func TestHeldOutboxReadIsOwnerOnlyAndDoesNotLoadTheQueue(t *testing.T) {
	d := newTestDevices(t)
	offlineMachine(d, "mac", "Studio Mac")
	store := newHeldMem()
	d.opts.Store = store // the queue hasn't been loaded by its delivery loop
	handler := d.Handler(http.NotFoundHandler())
	for _, auth := range [][2]string{{"", ""}, {"full", ""}, {"answer", "1"}, {"extension", "1"}} {
		if result := outboxRead(handler, outboxPath, auth[0], auth[1]); result.Code != http.StatusForbidden {
			t.Errorf("role %v: %d %s", auth, result.Code, result.Body)
		}
	}
	result := outboxRead(handler, outboxPath, "full", "1")
	if result.Code != http.StatusServiceUnavailable || !strings.Contains(result.Body.String(), "outbox_unavailable") ||
		store.loads != 0 || d.held.loaded {
		t.Fatalf("list of unloaded queue: %d %s (loads=%d, loaded=%v)", result.Code, result.Body, store.loads, d.held.loaded)
	}
}
