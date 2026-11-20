package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestHeldOutboxRouteUsesOfflineComputerOwner(t *testing.T) {
	h, m, manager := holdHub(t, proto.Session{})
	editSessions(m, func([]proto.Session) []proto.Session { return nil })
	mux := http.NewServeMux()
	h.deviceRoutesRelayed(mux)
	const path = "/api/v1/machines/box/held-messages"

	for _, tc := range []struct {
		name string
		who  principal
		want int
	}{
		{"master token", principal{Role: roleFull}, http.StatusForbidden},
		{"answer device", principal{Role: roleAnswer, ID: "test-watch"}, http.StatusForbidden},
		{"extension", principal{Role: roleExtension, ID: "test-extension"}, http.StatusForbidden},
		{"full device", ownerPhone, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, path+"?state=failed&limit=2", nil)
			r = r.WithContext(context.WithValue(r.Context(), principalKey{}, tc.who))
			r.Header.Set(assistantwire.PersonHeader, "1") // the hub must rely on the principal
			r.Header.Set("Authorization", "Bearer fixture-credential")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("got %d %s, want %d", w.Code, w.Body, tc.want)
			}
		})
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if len(manager.calls) != 0 || len(manager.relayed) != 1 {
		t.Fatalf("node calls %d, manager relays %d", len(manager.calls), len(manager.relayed))
	}
	relay := manager.relayed[0]
	if relay.Path != path || relay.Method != http.MethodGet || relay.Query != "state=failed&limit=2" ||
		len(relay.Header["Authorization"]) != 0 || len(relay.Header[assistantwire.PersonHeader]) != 1 {
		t.Fatalf("unexpected relay %+v", relay)
	}
}
