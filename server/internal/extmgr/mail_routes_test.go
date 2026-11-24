package extmgr

import (
	"net/http/httptest"
	"testing"
)

// Mail's routes are the manager's (relayed by the hub): their errors reach the apps as mail wrote them.
func TestMailRoutingErrorsUnchanged(t *testing.T) {
	h := newTestManager(t)
	h.mail = testMail(t)
	mux := h.routes()
	for _, tt := range []struct{ method, path string }{{"GET", "/api/v1/mail/missing"}, {"DELETE", "/api/v1/mail/accounts"}} {
		r := httptest.NewRequest(tt.method, tt.path, nil)
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Authorization", "Bearer "+h.cfg.Token)
		want := httptest.NewRecorder()
		h.mail.Handler().ServeHTTP(want, r)
		got := httptest.NewRecorder()
		mux.ServeHTTP(got, r)
		if got.Code != want.Code || got.Body.String() != want.Body.String() || got.Header().Get("Allow") != want.Header().Get("Allow") {
			t.Fatalf("mail error changed: %d %s", got.Code, got.Body.String())
		}
	}
}
