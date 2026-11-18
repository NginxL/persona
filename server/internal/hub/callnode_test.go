package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestCallNodeErrorsAndRawResult(t *testing.T) {
	rn := newRewindNode(t)
	m := rn.hub.byID["m1"]
	for _, tc := range []struct {
		name     string
		err      error
		status   int
		contains string
	}{
		{"outdated", errors.New("unknown method session.rewind"), 409, `"code":"node_outdated"`},
		{"refusal", &rpc.Error{Code: "rewind_busy", Message: "busy", Data: json.RawMessage(`{"detail":"test detail"}`)}, 409, `"detail":"test detail"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rn.mu.Lock()
			rn.err = tc.err
			rn.mu.Unlock()
			w := httptest.NewRecorder()
			_, ok := rn.hub.callNode(w, httptest.NewRequest("POST", "/", nil), m, "session.rewind", nil, time.Second, "hub-rewind.outdated", nil)
			if ok || w.Code != tc.status || !strings.Contains(w.Body.String(), tc.contains) {
				t.Fatalf("ok=%v status=%d body=%s", ok, w.Code, w.Body.String())
			}
		})
	}
	rn.mu.Lock()
	rn.err = nil
	rn.mu.Unlock()
	r := httptest.NewRequest("POST", "/", nil)
	ctx, cancel := context.WithDeadline(r.Context(), time.Now().Add(-time.Second))
	defer cancel()
	w := httptest.NewRecorder()
	if _, ok := rn.hub.callNode(w, r.WithContext(ctx), m, "session.rewind", nil, time.Second, "", nil); ok || w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), `"code":"timeout"`) {
		t.Fatalf("expired deadline: %d %s", w.Code, w.Body.String())
	}
	offline := newMachine(rn.hub, MachineConfig{ID: "offline", Name: "offline"})
	w = httptest.NewRecorder()
	if _, ok := rn.hub.callNode(w, r, offline, "session.rewind", nil, time.Second, "", nil); ok || w.Code != http.StatusServiceUnavailable {
		t.Fatalf("offline: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	writeRawJSON(w, nil)
	if w.Code != http.StatusOK || w.Body.Len() != 0 || w.Header().Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("empty raw result: %d %s", w.Code, w.Body.String())
	}
}

func TestNodeOutdatedMatchesPreviousResponse(t *testing.T) {
	h := newTestHub(t)
	for _, lang := range []string{"", "en", "ja", "zh-Hans"} {
		t.Run(lang, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.Header.Set("Accept-Language", lang)
			got, want := httptest.NewRecorder(), httptest.NewRecorder()
			key := "hub-fileedit.outdated"
			args := i18n.Args{"name": "computer"}
			if !h.nodeOutdated(got, r, &rpc.Error{Code: "unknown_method", Message: "unknown method file.rename"}, key, args) {
				t.Fatal("unknown method was not recognized")
			}
			writeCode(want, http.StatusConflict, proto.CodeNodeOutdated, i18n.T(h.requestLang(r), key, args), nil)
			if got.Code != want.Code || got.Body.String() != want.Body.String() {
				t.Fatalf("got %d %q, want %d %q", got.Code, got.Body.String(), want.Code, want.Body.String())
			}
		})
	}
}
