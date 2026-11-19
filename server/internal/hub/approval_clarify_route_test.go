package hub

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

func TestClarifyRouteRejectsNonOwnerPrincipalsBeforeRelay(t *testing.T) {
	h := newTestHub(t)
	mux := http.NewServeMux()
	h.fleetRoutesRelayed(mux)
	for _, role := range []string{roleAnswer, roleExtension} {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/interactions/ix_owner_only/clarify",
			strings.NewReader(`{"text":"Why?","client_msg_id":"owner-only-question"}`))
		r = r.WithContext(context.WithValue(r.Context(), principalKey{}, principal{ID: "restricted", Role: role, Manager: assistantwire.AssistantID}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("%s clarify = %d %s, want 403", role, w.Code, w.Body)
		}
	}
}

func TestClarifyClientOperationCanRetryOnlyUncertainDelivery(t *testing.T) {
	for _, tc := range []struct {
		code string
		want bool
	}{{assistantwire.CodeAssistantOffline, true}, {assistantwire.CodeAssistantInDoubt, true}, {"conversation_deleted", false}, {"", false}} {
		body := []byte(`{"code":"` + tc.code + `"}`)
		if got := clientOpRetry("clarify", body); got != tc.want {
			t.Errorf("clarify retry for %q = %v, want %v", tc.code, got, tc.want)
		}
		if got := clientOpRetry("resolve", body); got {
			t.Errorf("resolve unexpectedly retries %q", tc.code)
		}
	}
}
